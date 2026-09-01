package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"

	"github.com/poulsena/delegatd/internal/domain"
	"github.com/poulsena/delegatd/internal/policy"
	"go.yaml.in/yaml/v3"
)

const maxConfigurationSize = 1 << 20

var instanceNamePattern = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)
var environmentInterpolationPattern = regexp.MustCompile(`\$[A-Za-z_{]`)

// Document is the loaded deployment configuration and its absolute location.
type Document struct {
	Path   string
	Dir    string
	Config Config
}

// StoreDocument is the store-only projection used by offline inspection.
type StoreDocument struct {
	Path    string
	Dir     string
	Version int
	Store   Instance
}

// Config is the version-one deployment envelope.
type Config struct {
	Version            int                  `yaml:"version"`
	Store              Instance             `yaml:"store"`
	Connectors         map[string]Instance  `yaml:"connectors"`
	WorkspaceProviders map[string]Instance  `yaml:"workspace_providers"`
	AgentRuntimes      map[string]Instance  `yaml:"agent_runtimes"`
	Resources          map[string]Resource  `yaml:"resources"`
	Policy             domain.PolicyRequest `yaml:"policy"`
}

// Resource is a logical allowlisted resource bound to a connector instance.
type Resource struct {
	Kind      domain.ResourceKind `yaml:"kind"`
	Connector string              `yaml:"connector"`
	Config    yaml.Node           `yaml:"config"`
}

// Instance selects one compiled adapter and owns its adapter-specific node.
type Instance struct {
	Kind   string    `yaml:"kind"`
	Config yaml.Node `yaml:"config"`
}

// ValidationError carries a stable, non-sensitive configuration reason.
type ValidationError struct {
	reason string
	cause  error
}

func (e *ValidationError) Error() string {
	if e == nil {
		return ""
	}
	return e.reason
}

func (e *ValidationError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

func validationError(reason string, cause error) error {
	return &ValidationError{reason: reason, cause: cause}
}

// SafeReason returns the normalized reason for a configuration error.
func SafeReason(err error) string {
	if err == nil {
		return ""
	}
	var validation *ValidationError
	if errors.As(err, &validation) {
		return validation.reason
	}
	return "configuration YAML is invalid"
}

// Load reads and validates one version-one deployment configuration.
func Load(path string) (Document, error) {
	absolutePath, dir, root, err := readRoot(path, true)
	if err != nil {
		return Document{}, err
	}
	if containsEnvironmentInterpolation(root) {
		return Document{}, validationError("configuration YAML is invalid", nil)
	}

	var cfg Config
	if err := Decode(root, &cfg); err != nil {
		return Document{}, validationError("configuration YAML is invalid", err)
	}
	if cfg.Version != 1 {
		return Document{}, validationError("version must be 1", nil)
	}
	if _, ok := mappingValue(root, "store"); !ok {
		return Document{}, validationError("store is required", nil)
	}
	if _, ok := mappingValue(root, "connectors"); !ok {
		return Document{}, validationError("connectors is required", nil)
	} else if len(cfg.Connectors) == 0 {
		return Document{}, validationError("connectors must contain at least one instance", nil)
	}
	if _, ok := mappingValue(root, "workspace_providers"); !ok {
		return Document{}, validationError("workspace_providers is required", nil)
	} else if len(cfg.WorkspaceProviders) == 0 {
		return Document{}, validationError("workspace_providers must contain at least one instance", nil)
	}
	if _, ok := mappingValue(root, "agent_runtimes"); !ok {
		return Document{}, validationError("agent_runtimes is required", nil)
	} else if len(cfg.AgentRuntimes) == 0 {
		return Document{}, validationError("agent_runtimes must contain at least one instance", nil)
	}

	if err := validateInstance("store", cfg.Store); err != nil {
		return Document{}, err
	}
	if err := validateInstances("connectors", "connector", cfg.Connectors); err != nil {
		return Document{}, err
	}
	if err := validateInstances("workspace_providers", "workspace_provider", cfg.WorkspaceProviders); err != nil {
		return Document{}, err
	}
	if err := validateInstances("agent_runtimes", "agent_runtime", cfg.AgentRuntimes); err != nil {
		return Document{}, err
	}
	normalizedPolicy, err := policy.NormalizeRequest(cfg.Policy)
	if err != nil {
		return Document{}, validationError("configuration YAML is invalid", err)
	}
	cfg.Policy = normalizedPolicy
	if err := validateResources(cfg); err != nil {
		return Document{}, err
	}

	return Document{Path: absolutePath, Dir: dir, Config: cfg}, nil
}

// LoadStore reads only the deployment version and store projection. All
// unrelated top-level fields are intentionally ignored for offline inspection.
func LoadStore(path string) (StoreDocument, error) {
	absolutePath, dir, root, err := readRoot(path, false)
	if err != nil {
		return StoreDocument{}, err
	}
	version, versionOK := mappingValue(root, "version")
	store, storeOK := mappingValue(root, "store")
	if !versionOK || !storeOK || mappingCount(root, "version") != 1 || mappingCount(root, "store") != 1 {
		return StoreDocument{}, validationError("configuration YAML is invalid", nil)
	}
	if containsAlias(version) || containsAlias(store) || containsEnvironmentInterpolation(store) {
		return StoreDocument{}, validationError("configuration YAML is invalid", nil)
	}
	projection := yaml.Node{Kind: yaml.MappingNode, Content: []*yaml.Node{
		scalarNode("version"), &version,
		scalarNode("store"), &store,
	}}
	var decoded struct {
		Version int      `yaml:"version"`
		Store   Instance `yaml:"store"`
	}
	if err := Decode(projection, &decoded); err != nil {
		return StoreDocument{}, validationError("configuration YAML is invalid", err)
	}
	if decoded.Version != 1 {
		return StoreDocument{}, validationError("version must be 1", nil)
	}
	if err := validateInstance("store", decoded.Store); err != nil {
		return StoreDocument{}, err
	}
	return StoreDocument{Path: absolutePath, Dir: dir, Version: decoded.Version, Store: decoded.Store}, nil
}

func readRoot(path string, rejectAliases bool) (string, string, yaml.Node, error) {
	absolutePath, err := filepath.Abs(path)
	if err != nil {
		return "", "", yaml.Node{}, validationError("configuration file is unreadable", err)
	}
	file, err := os.Open(absolutePath)
	if err != nil {
		return "", "", yaml.Node{}, validationError("configuration file is unreadable", err)
	}
	data, readErr := io.ReadAll(io.LimitReader(file, maxConfigurationSize+1))
	closeErr := file.Close()
	if readErr != nil || closeErr != nil {
		if readErr != nil {
			return "", "", yaml.Node{}, validationError("configuration file is unreadable", readErr)
		}
		return "", "", yaml.Node{}, validationError("configuration file is unreadable", closeErr)
	}
	if len(data) > maxConfigurationSize {
		return "", "", yaml.Node{}, validationError("configuration exceeds 1 MiB", nil)
	}
	root, err := decodeSingleDocument(data, rejectAliases)
	if err != nil {
		return "", "", yaml.Node{}, err
	}
	return absolutePath, filepath.Dir(absolutePath), root, nil
}
func Decode(node yaml.Node, dst any) error {
	if node.Kind == yaml.DocumentNode {
		if len(node.Content) != 1 {
			return fmt.Errorf("configuration must be a mapping")
		}
		node = *node.Content[0]
	}
	if node.Kind != yaml.MappingNode {
		return fmt.Errorf("configuration must be a mapping")
	}
	data, err := yaml.Marshal(&node)
	if err != nil {
		return err
	}
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(dst); err != nil {
		return err
	}
	var extra yaml.Node
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return fmt.Errorf("configuration contains multiple documents")
		}
		return err
	}
	return nil
}

func decodeSingleDocument(data []byte, rejectAliases bool) (yaml.Node, error) {
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	var document yaml.Node
	if err := decoder.Decode(&document); err != nil {
		return yaml.Node{}, validationError("configuration YAML is invalid", err)
	}
	if document.Kind != yaml.DocumentNode || len(document.Content) != 1 {
		return yaml.Node{}, validationError("configuration YAML is invalid", nil)
	}
	root := *document.Content[0]
	if root.Kind != yaml.MappingNode {
		return yaml.Node{}, validationError("configuration YAML is invalid", nil)
	}
	if rejectAliases && containsAlias(root) {
		return yaml.Node{}, validationError("configuration YAML is invalid", nil)
	}

	var extra yaml.Node
	err := decoder.Decode(&extra)
	if err == nil {
		return yaml.Node{}, validationError("configuration must contain exactly one document", nil)
	}
	if err != io.EOF {
		return yaml.Node{}, validationError("configuration YAML is invalid", err)
	}
	return root, nil
}

func mappingValue(node yaml.Node, key string) (yaml.Node, bool) {
	if node.Kind != yaml.MappingNode {
		return yaml.Node{}, false
	}
	for index := 0; index+1 < len(node.Content); index += 2 {
		if node.Content[index].Value == key {
			if node.Content[index+1] == nil {
				return yaml.Node{}, false
			}
			return *node.Content[index+1], true
		}
	}
	return yaml.Node{}, false
}

func mappingCount(node yaml.Node, key string) int {
	if node.Kind != yaml.MappingNode {
		return 0
	}
	count := 0
	for index := 0; index+1 < len(node.Content); index += 2 {
		if node.Content[index].Value == key {
			count++
		}
	}
	return count
}

func scalarNode(value string) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value}
}

func containsAlias(node yaml.Node) bool {
	if node.Kind == yaml.AliasNode {
		return true
	}
	for _, child := range node.Content {
		if child != nil && containsAlias(*child) {
			return true
		}
	}
	return false
}

func containsEnvironmentInterpolation(node yaml.Node) bool {
	if node.Kind == yaml.ScalarNode && environmentInterpolationPattern.MatchString(node.Value) {
		return true
	}
	for _, child := range node.Content {
		if child != nil && containsEnvironmentInterpolation(*child) {
			return true
		}
	}
	return false
}

func validateResources(cfg Config) error {
	names := make([]string, 0, len(cfg.Resources))
	for name := range cfg.Resources {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		resource := cfg.Resources[name]
		if !instanceNamePattern.MatchString(name) {
			return validationError("resources contains an invalid instance name", nil)
		}
		checkID := "resource." + name
		if !validKind(string(resource.Kind)) {
			return validationError(checkID+" kind is missing or invalid", nil)
		}
		if !instanceNamePattern.MatchString(resource.Connector) {
			return validationError(checkID+" connector is missing or invalid", nil)
		}
		if _, ok := cfg.Connectors[resource.Connector]; !ok {
			return validationError(checkID+" connector is unknown", nil)
		}
		if resource.Config.Kind != yaml.MappingNode {
			return validationError(checkID+" configuration is invalid", nil)
		}
	}
	return nil
}

func validateInstance(section string, instance Instance) error {
	if !validKind(instance.Kind) {
		return validationError(fmt.Sprintf("%s kind is missing or invalid", section), nil)
	}
	return nil
}

func validateInstances(section, checkPrefix string, instances map[string]Instance) error {
	names := make([]string, 0, len(instances))
	for name := range instances {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		instance := instances[name]
		if !instanceNamePattern.MatchString(name) {
			return validationError(section+" contains an invalid instance name", nil)
		}
		checkID := checkPrefix + "." + name
		if !validKind(instance.Kind) {
			return validationError(checkID+" kind is missing or invalid", nil)
		}
	}
	return nil
}

func validKind(kind string) bool {
	return instanceNamePattern.MatchString(kind)
}
