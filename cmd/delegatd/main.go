package main

import (
	"context"
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
	"github.com/poulsena/delegatd/internal/doctor"
)

const usage = "usage: delegatd doctor --config FILE [--timeout DURATION]"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	exitCode := run(ctx, os.Args[1:], os.Stdout, os.Stderr, bootstrap.LoadDoctor)
	stop()
	os.Exit(exitCode)
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer, load func(string) (config.Document, []doctor.Check, *doctor.Failure)) int {
	if len(args) == 0 || args[0] != "doctor" {
		writeUsage(stderr)
		return 2
	}

	flags := flag.NewFlagSet("doctor", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	configPath := flags.String("config", "", "configuration file")
	timeoutValue := flags.String("timeout", "10s", "diagnostic timeout")
	help := flags.Bool("help", false, "show help")
	shortHelp := flags.Bool("h", false, "show help")
	if err := flags.Parse(args[1:]); err != nil {
		writeUsage(stderr)
		return 2
	}
	if *help || *shortHelp {
		fmt.Fprintln(stdout, usage)
		return 0
	}
	if *configPath == "" || len(flags.Args()) != 0 {
		writeUsage(stderr)
		return 2
	}
	timeout, err := time.ParseDuration(*timeoutValue)
	if err != nil || timeout <= 0 {
		writeUsage(stderr)
		return 2
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

func safeReason(failure *doctor.Failure) string {
	if failure == nil || strings.TrimSpace(failure.Reason) == "" {
		return "diagnostic failed"
	}
	return failure.Reason
}

func writeUsage(stderr io.Writer) {
	fmt.Fprintln(stderr, usage)
}
