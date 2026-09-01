package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/poulsena/delegatd/internal/bootstrap"
	"github.com/poulsena/delegatd/internal/config"
	"github.com/poulsena/delegatd/internal/control"
	"github.com/poulsena/delegatd/internal/doctor"
	"github.com/poulsena/delegatd/internal/domain"
)

const (
	doctorUsage      = "usage: delegatd doctor --config FILE [--timeout DURATION]"
	taskSubmitUsage  = "usage: delegatd task submit --config FILE --resource NAME (--input TEXT | --input-file FILE)"
	taskShowUsage    = "usage: delegatd task show --config FILE TASK_ID"
	taskUsage        = taskSubmitUsage + "\n" + taskShowUsage
	rootUsage        = doctorUsage + "\n" + taskUsage
	maxTaskInputSize = 1 << 20
)

type commandDependencies struct {
	loadDoctor func(string) (config.Document, []doctor.Check, *doctor.Failure)
	submitTask func(context.Context, string, string, domain.TaskInput) (domain.Task, error)
	showTask   func(context.Context, string, domain.TaskID) (domain.Task, error)
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	exitCode := run(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr, commandDependencies{
		loadDoctor: bootstrap.LoadDoctor,
		submitTask: bootstrap.SubmitTask,
		showTask:   bootstrap.ShowTask,
	})
	stop()
	os.Exit(exitCode)
}

func run(ctx context.Context, args []string, stdin io.ReadCloser, stdout, stderr io.Writer, dependencies commandDependencies) int {
	if len(args) == 0 {
		writeRootUsage(stderr)
		return 2
	}
	switch args[0] {
	case "doctor":
		return runDoctor(ctx, args[1:], stdout, stderr, dependencies.loadDoctor)
	case "task":
		return runTask(ctx, args[1:], stdin, stdout, stderr, dependencies)
	default:
		writeRootUsage(stderr)
		return 2
	}
}

func runDoctor(ctx context.Context, args []string, stdout, stderr io.Writer, load func(string) (config.Document, []doctor.Check, *doctor.Failure)) int {
	flags := flag.NewFlagSet("doctor", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	configPath := flags.String("config", "", "configuration file")
	timeoutValue := flags.String("timeout", "10s", "diagnostic timeout")
	help := flags.Bool("help", false, "show help")
	shortHelp := flags.Bool("h", false, "show help")
	if err := flags.Parse(args); err != nil {
		writeDoctorUsage(stderr)
		return 2
	}
	if *help || *shortHelp {
		fmt.Fprintln(stdout, doctorUsage)
		return 0
	}
	if *configPath == "" || len(flags.Args()) != 0 {
		writeDoctorUsage(stderr)
		return 2
	}
	timeout, err := time.ParseDuration(*timeoutValue)
	if err != nil || timeout <= 0 {
		writeDoctorUsage(stderr)
		return 2
	}

	if load == nil {
		fmt.Fprintln(stdout, "FAIL config: configuration loader is unavailable\nFAIL doctor: configuration invalid")
		return 1
	}
	document, checks, failure := load(*configPath)
	if failure != nil {
		fmt.Fprintf(stdout, "FAIL config: %s\nFAIL doctor: configuration invalid\n", safeReason(failure))
		return 1
	}

	checkContext, cancel := context.WithTimeout(ctx, timeout)
	report := doctor.Run(checkContext, checks)
	cancel()

	fmt.Fprintf(stdout, "PASS config: schema version %d\n", document.Config.Version)
	failed := 0
	for _, result := range report.Results {
		if result.Failure != nil {
			failed++
			fmt.Fprintf(stdout, "FAIL %s: %s\n", result.ID, safeReason(result.Failure))
			continue
		}
		fmt.Fprintf(stdout, "PASS %s: %s\n", result.ID, result.Detail)
	}
	if failed == 0 {
		fmt.Fprintf(stdout, "PASS doctor: %d checks passed\n", len(report.Results))
		return 0
	}
	fmt.Fprintf(stdout, "FAIL doctor: %d of %d checks failed\n", failed, len(report.Results))
	return 1
}

func runTask(ctx context.Context, args []string, stdin io.ReadCloser, stdout, stderr io.Writer, dependencies commandDependencies) int {
	if len(args) == 0 {
		writeTaskUsage(stderr)
		return 2
	}
	if args[0] == "--help" || args[0] == "-h" {
		fmt.Fprintln(stdout, taskUsage)
		return 0
	}
	switch args[0] {
	case "submit":
		return runTaskSubmit(ctx, args[1:], stdin, stdout, stderr, dependencies.submitTask)
	case "show":
		return runTaskShow(ctx, args[1:], stdout, stderr, dependencies.showTask)
	default:
		writeTaskUsage(stderr)
		return 2
	}
}

func runTaskSubmit(ctx context.Context, args []string, stdin io.ReadCloser, stdout, stderr io.Writer, submit func(context.Context, string, string, domain.TaskInput) (domain.Task, error)) int {
	flags := flag.NewFlagSet("task submit", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	configPath := flags.String("config", "", "configuration file")
	resourceName := flags.String("resource", "", "logical resource name")
	var inputValue, inputFile string
	inputCount, inputFileCount := 0, 0
	flags.Func("input", "inline task input", func(value string) error {
		inputValue = value
		inputCount++
		return nil
	})
	flags.Func("input-file", "task input file, or - for stdin", func(value string) error {
		inputFile = value
		inputFileCount++
		return nil
	})
	help := flags.Bool("help", false, "show help")
	shortHelp := flags.Bool("h", false, "show help")
	if err := flags.Parse(args); err != nil {
		writeTaskSubmitUsage(stderr)
		return 2
	}
	if *help || *shortHelp {
		fmt.Fprintln(stdout, taskSubmitUsage)
		return 0
	}
	if *configPath == "" || *resourceName == "" || len(flags.Args()) != 0 || inputCount+inputFileCount != 1 {
		writeTaskSubmitUsage(stderr)
		return 2
	}
	input, err := readTaskInput(ctx, inputValue, inputFile, stdin)
	if err != nil {
		return writeTaskFailure(stdout, err)
	}
	if submit == nil {
		return writeTaskFailure(stdout, control.NewFailure("task failed", errors.New("submit command is unavailable")))
	}
	task, err := submit(ctx, *configPath, *resourceName, input)
	if err != nil {
		return writeTaskFailure(stdout, err)
	}
	if err := writeJSON(stdout, struct {
		TaskID domain.TaskID `json:"task_id"`
	}{TaskID: task.ID}); err != nil {
		return writeTaskFailure(stdout, err)
	}
	return 0
}

func runTaskShow(ctx context.Context, args []string, stdout, stderr io.Writer, show func(context.Context, string, domain.TaskID) (domain.Task, error)) int {
	flags := flag.NewFlagSet("task show", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	configPath := flags.String("config", "", "configuration file")
	help := flags.Bool("help", false, "show help")
	shortHelp := flags.Bool("h", false, "show help")
	if err := flags.Parse(args); err != nil {
		writeTaskShowUsage(stderr)
		return 2
	}
	if *help || *shortHelp {
		fmt.Fprintln(stdout, taskShowUsage)
		return 0
	}
	if *configPath == "" || len(flags.Args()) != 1 {
		writeTaskShowUsage(stderr)
		return 2
	}
	id, err := domain.ParseTaskID(flags.Args()[0])
	if err != nil {
		writeTaskShowUsage(stderr)
		return 2
	}
	if show == nil {
		return writeTaskFailure(stdout, control.NewFailure("task failed", errors.New("show command is unavailable")))
	}
	task, err := show(ctx, *configPath, id)
	if err != nil {
		return writeTaskFailure(stdout, err)
	}
	if err := writeJSON(stdout, task); err != nil {
		return writeTaskFailure(stdout, err)
	}
	return 0
}

func readTaskInput(ctx context.Context, inlineValue, fileValue string, stdin io.ReadCloser) (domain.TaskInput, error) {
	var raw []byte
	if fileValue == "" {
		raw = []byte(inlineValue)
	} else if fileValue == "-" {
		if stdin == nil {
			return domain.TaskInput{}, control.NewFailure("task input is unreadable", errors.New("stdin is unavailable"))
		}
		type result struct {
			data []byte
			err  error
		}
		completed := make(chan result, 1)
		go func() {
			data, err := io.ReadAll(io.LimitReader(stdin, maxTaskInputSize+1))
			completed <- result{data: data, err: err}
		}()
		select {
		case output := <-completed:
			if output.err != nil {
				return domain.TaskInput{}, control.NewFailure("task input is unreadable", output.err)
			}
			if len(output.data) > maxTaskInputSize {
				return domain.TaskInput{}, control.NewFailure("task input exceeds 1 MiB", nil)
			}
			raw = output.data
		case <-ctx.Done():
			_ = stdin.Close()
			<-completed
			return domain.TaskInput{}, control.NewFailure("task cancelled", ctx.Err())
		}
	} else {
		info, err := os.Stat(fileValue)
		if err != nil || !info.Mode().IsRegular() {
			if err == nil {
				err = errors.New("task input is not a regular file")
			}
			return domain.TaskInput{}, control.NewFailure("task input is unreadable", err)
		}
		if info.Size() > maxTaskInputSize {
			return domain.TaskInput{}, control.NewFailure("task input exceeds 1 MiB", nil)
		}
		file, err := os.Open(fileValue)
		if err != nil {
			return domain.TaskInput{}, control.NewFailure("task input is unreadable", err)
		}
		raw, err = io.ReadAll(io.LimitReader(file, maxTaskInputSize+1))
		closeErr := file.Close()
		if err != nil || closeErr != nil {
			if err != nil {
				return domain.TaskInput{}, control.NewFailure("task input is unreadable", err)
			}
			return domain.TaskInput{}, control.NewFailure("task input is unreadable", closeErr)
		}
		if len(raw) > maxTaskInputSize {
			return domain.TaskInput{}, control.NewFailure("task input exceeds 1 MiB", nil)
		}
	}
	input, err := domain.NormalizeManualInput(raw)
	if err != nil {
		reason := "task input is invalid"
		if strings.Contains(err.Error(), "exceeds") {
			reason = "task input exceeds 1 MiB"
		}
		return domain.TaskInput{}, control.NewFailure(reason, err)
	}
	return input, nil
}

func writeJSON(writer io.Writer, value any) error {
	encoder := json.NewEncoder(writer)
	encoder.SetEscapeHTML(false)
	return encoder.Encode(value)
}

func writeTaskFailure(stdout io.Writer, err error) int {
	fmt.Fprintf(stdout, "FAIL task: %s\n", control.SafeReason(err))
	return 1
}

func safeReason(failure *doctor.Failure) string {
	if failure == nil || strings.TrimSpace(failure.Reason) == "" {
		return "diagnostic failed"
	}
	return failure.Reason
}

func writeDoctorUsage(writer io.Writer) {
	fmt.Fprintln(writer, doctorUsage)
}

func writeTaskSubmitUsage(writer io.Writer) {
	fmt.Fprintln(writer, taskSubmitUsage)
}

func writeTaskShowUsage(writer io.Writer) {
	fmt.Fprintln(writer, taskShowUsage)
}

func writeTaskUsage(writer io.Writer) {
	fmt.Fprintln(writer, taskUsage)
}

func writeRootUsage(writer io.Writer) {
	fmt.Fprintln(writer, rootUsage)
}
