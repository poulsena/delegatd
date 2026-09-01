package bootstrap

import (
	"context"
	"errors"

	"github.com/poulsena/delegatd/internal/config"
	"github.com/poulsena/delegatd/internal/connector/github"
	"github.com/poulsena/delegatd/internal/control"
	"github.com/poulsena/delegatd/internal/domain"
	"github.com/poulsena/delegatd/internal/store/sqlite"
)

type taskOptions struct {
	openStore           func(context.Context, sqlite.Config, string) (*sqlite.Store, error)
	openStoreReadOnly   func(context.Context, sqlite.Config, string) (*sqlite.Store, error)
	newRepositorySource func(github.Config, github.RepositoryConfig, string) (control.RepositorySource, error)
	newID               func(string) string
}

func defaultTaskOptions() taskOptions {
	return taskOptions{
		openStore: func(ctx context.Context, cfg sqlite.Config, dir string) (*sqlite.Store, error) {
			return sqlite.Open(ctx, cfg, dir)
		},
		openStoreReadOnly: func(ctx context.Context, cfg sqlite.Config, dir string) (*sqlite.Store, error) {
			return sqlite.OpenReadOnly(ctx, cfg, dir)
		},
		newRepositorySource: func(app github.Config, resource github.RepositoryConfig, dir string) (control.RepositorySource, error) {
			return github.NewRepositorySource(app, resource, dir)
		},
	}
}

// SubmitTask loads the full deployment, snapshots the selected repository and
// policy, and records one pending task without starting a worker.
func SubmitTask(ctx context.Context, configPath, resourceName string, input domain.TaskInput) (domain.Task, error) {
	return submitTask(ctx, configPath, resourceName, input, defaultTaskOptions())
}

func submitTask(ctx context.Context, configPath, resourceName string, input domain.TaskInput, options taskOptions) (domain.Task, error) {
	if options.openStore == nil || options.newRepositorySource == nil {
		defaults := defaultTaskOptions()
		if options.openStore == nil {
			options.openStore = defaults.openStore
		}
		if options.newRepositorySource == nil {
			options.newRepositorySource = defaults.newRepositorySource
		}
	}
	document, err := config.Load(configPath)
	if err != nil {
		return domain.Task{}, control.NewFailure(config.SafeReason(err), err)
	}
	resource, ok := document.Config.Resources[resourceName]
	if !ok {
		return domain.Task{}, control.NewFailure("resource is not configured", errors.New("resource alias is absent"))
	}
	if resource.Kind != domain.ResourceKindRepository || resource.Connector == "" {
		return domain.Task{}, control.NewFailure("resource is unsupported", errors.New("resource kind or connector is unsupported"))
	}
	connector, ok := document.Config.Connectors[resource.Connector]
	if !ok || connector.Kind != "github" {
		return domain.Task{}, control.NewFailure("resource is unsupported", errors.New("resource connector is unsupported"))
	}
	var appConfig github.Config
	if err := config.Decode(connector.Config, &appConfig); err != nil {
		return domain.Task{}, control.NewFailure("connector configuration is invalid", err)
	}
	var repositoryConfig github.RepositoryConfig
	if err := config.Decode(resource.Config, &repositoryConfig); err != nil {
		return domain.Task{}, control.NewFailure("resource configuration is invalid", err)
	}
	var storeConfig sqlite.Config
	if err := config.Decode(document.Config.Store.Config, &storeConfig); err != nil {
		return domain.Task{}, control.NewFailure("state store is unavailable", err)
	}
	store, err := options.openStore(ctx, storeConfig, document.Dir)
	if err != nil {
		return domain.Task{}, controlFromError(err, "state store is unavailable")
	}
	defer store.Close()
	source, err := options.newRepositorySource(appConfig, repositoryConfig, document.Dir)
	if err != nil {
		return domain.Task{}, control.NewFailure("resource configuration is invalid", err)
	}
	service := control.NewTaskService(store, options.newID, nil)
	return service.SubmitManualRepository(ctx, resourceName, resource.Connector, input, document.Config.Policy, source)
}

// ShowTask reads the task row through a store-only projection. It does not
// validate, instantiate, or contact any current connector/resource adapter.
func ShowTask(ctx context.Context, configPath string, id domain.TaskID) (domain.Task, error) {
	return showTask(ctx, configPath, id, defaultTaskOptions())
}

func showTask(ctx context.Context, configPath string, id domain.TaskID, options taskOptions) (domain.Task, error) {
	if options.openStoreReadOnly == nil {
		options.openStoreReadOnly = defaultTaskOptions().openStoreReadOnly
	}
	document, err := config.LoadStore(configPath)
	if err != nil {
		return domain.Task{}, control.NewFailure(config.SafeReason(err), err)
	}
	if document.Store.Kind != "sqlite" {
		return domain.Task{}, control.NewFailure("state store is unavailable", errors.New("store adapter is unsupported"))
	}
	var storeConfig sqlite.Config
	if err := config.Decode(document.Store.Config, &storeConfig); err != nil {
		return domain.Task{}, control.NewFailure("state store is unavailable", err)
	}
	store, err := options.openStoreReadOnly(ctx, storeConfig, document.Dir)
	if err != nil {
		return domain.Task{}, controlFromError(err, "state store is unavailable")
	}
	defer store.Close()
	service := control.NewTaskService(store, nil, nil)
	return service.Show(ctx, id)
}

func controlFromError(err error, fallback string) error {
	if err == nil {
		return nil
	}
	var reasonProvider interface{ SafeReason() string }
	if errors.As(err, &reasonProvider) && reasonProvider.SafeReason() != "" {
		return control.NewFailure(reasonProvider.SafeReason(), err)
	}
	return control.NewFailure(fallback, err)
}
