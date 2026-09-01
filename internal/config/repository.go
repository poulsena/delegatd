package config

import (
	"errors"
	"strings"
	"time"

	"github.com/poulsena/delegatd/internal/domain"
	"github.com/poulsena/delegatd/internal/policy"
)

// DecodeRepository decodes the repository-owned version-one configuration.
// Repository commands are data here; they are never executed by this package.
func DecodeRepository(data []byte) (domain.RepositoryConfiguration, error) {
	if len(data) > maxConfigurationSize {
		return domain.RepositoryConfiguration{}, errors.New("repository configuration exceeds 1 MiB")
	}
	root, err := decodeSingleDocument(data, true)
	if err != nil {
		return domain.RepositoryConfiguration{}, err
	}
	var configuration domain.RepositoryConfiguration
	if err := Decode(root, &configuration); err != nil {
		return domain.RepositoryConfiguration{}, err
	}
	if configuration.Version != 1 {
		return domain.RepositoryConfiguration{}, errors.New("repository configuration version must be 1")
	}

	instructions := make([]string, 0, len(configuration.Agent.Instructions))
	seenInstructions := make(map[string]struct{}, len(configuration.Agent.Instructions))
	for _, raw := range configuration.Agent.Instructions {
		instruction, err := policy.NormalizeInstructionPath(raw)
		if err != nil {
			return domain.RepositoryConfiguration{}, err
		}
		if _, exists := seenInstructions[instruction]; exists {
			return domain.RepositoryConfiguration{}, errors.New("duplicate instruction path")
		}
		seenInstructions[instruction] = struct{}{}
		instructions = append(instructions, instruction)
	}
	configuration.Agent.Instructions = instructions

	normalizedPolicy, err := policy.NormalizeRequest(configuration.Policy)
	if err != nil {
		return domain.RepositoryConfiguration{}, err
	}
	configuration.Policy = normalizedPolicy

	validation := make([]domain.ValidationCommand, 0, len(configuration.Validation.Required))
	seenValidation := make(map[string]struct{}, len(configuration.Validation.Required))
	for _, command := range configuration.Validation.Required {
		name := strings.TrimSpace(command.Name)
		if !instanceNamePattern.MatchString(name) {
			return domain.RepositoryConfiguration{}, errors.New("validation name is invalid")
		}
		if _, exists := seenValidation[name]; exists {
			return domain.RepositoryConfiguration{}, errors.New("duplicate validation name")
		}
		seenValidation[name] = struct{}{}
		if strings.TrimSpace(command.Run) == "" {
			return domain.RepositoryConfiguration{}, errors.New("validation command is empty")
		}
		if command.Timeout == "" {
			return domain.RepositoryConfiguration{}, errors.New("validation timeout is required")
		}
		duration, err := time.ParseDuration(command.Timeout)
		if err != nil || duration <= 0 {
			return domain.RepositoryConfiguration{}, errors.New("validation timeout is invalid")
		}
		validation = append(validation, domain.ValidationCommand{
			Name:    name,
			Run:     command.Run,
			Timeout: duration.String(),
		})
	}
	configuration.Validation.Required = validation
	return configuration, nil
}
