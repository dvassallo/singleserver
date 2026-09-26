package singleserver

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

const (
	deployOverlayPath          = ".singleserver/deploy.yml"
	repoDeployConfigPath       = "config/deploy.yml"
	mergeDeployConfigCommand   = "merge-deploy-config"
	generatedDeployYAMLEnv     = "SINGLESERVER_GENERATED_DEPLOY_YML"
	deployOverlayConfigSource  = "generated + repo overlay " + deployOverlayPath
	generatedDeployConfigLabel = "generated from conventions"
)

var managedDeployKeys = []string{"service", "image", "servers", "ssh", "registry"}

func mergeDeployOverlay(generated []byte, overlay []byte) ([]byte, error) {
	var overlayDoc yaml.Node
	if err := yaml.Unmarshal(overlay, &overlayDoc); err != nil {
		return nil, fmt.Errorf("%s: %w", deployOverlayPath, err)
	}
	if len(overlayDoc.Content) == 0 {
		return generated, nil
	}
	overlayRoot := resolveAliases(overlayDoc.Content[0])
	if overlayRoot.Kind == yaml.ScalarNode && overlayRoot.Tag == "!!null" {
		return generated, nil
	}
	if overlayRoot.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("%s must be a mapping of Kamal config keys", deployOverlayPath)
	}
	if err := rejectManagedDeployKeys(overlayRoot); err != nil {
		return nil, err
	}

	var generatedDoc yaml.Node
	if err := yaml.Unmarshal(generated, &generatedDoc); err != nil {
		return nil, err
	}
	mergeMappingNodes(generatedDoc.Content[0], overlayRoot)

	var buf bytes.Buffer
	encoder := yaml.NewEncoder(&buf)
	encoder.SetIndent(2)
	if err := encoder.Encode(&generatedDoc); err != nil {
		return nil, err
	}
	if err := encoder.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func rejectManagedDeployKeys(overlay *yaml.Node) error {
	for i := 0; i < len(overlay.Content); i += 2 {
		key := overlay.Content[i].Value
		for _, managed := range managedDeployKeys {
			if key == managed {
				return fmt.Errorf("%s sets %q, which Single Server manages", deployOverlayPath, key)
			}
		}
	}
	return nil
}

func mergeMappingNodes(base *yaml.Node, overlay *yaml.Node) {
	for i := 0; i < len(overlay.Content); i += 2 {
		key, value := overlay.Content[i], overlay.Content[i+1]
		existing := mappingValue(base, key.Value)
		switch {
		case existing == nil:
			base.Content = append(base.Content, key, value)
		case existing.Kind == yaml.MappingNode && value.Kind == yaml.MappingNode:
			mergeMappingNodes(existing, value)
		default:
			*existing = *value
		}
	}
}

func mappingValue(mapping *yaml.Node, key string) *yaml.Node {
	for i := 0; i < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == key {
			return mapping.Content[i+1]
		}
	}
	return nil
}

func resolveAliases(node *yaml.Node) *yaml.Node {
	if node.Kind == yaml.AliasNode {
		return resolveAliases(node.Alias)
	}
	resolved := *node
	resolved.Anchor = ""
	resolved.Content = make([]*yaml.Node, len(node.Content))
	for i, child := range node.Content {
		resolved.Content[i] = resolveAliases(child)
	}
	return &resolved
}

func repoDeployOverlay(repoDir string) ([]byte, bool, error) {
	if _, err := os.Stat(filepath.Join(repoDir, ".git")); err != nil {
		return nil, false, nil
	}
	if gitRun(repoDir, "ls-files", "--error-unmatch", repoDeployConfigPath) == nil {
		return nil, false, nil
	}
	if gitRun(repoDir, "ls-files", "--error-unmatch", deployOverlayPath) != nil {
		return nil, false, nil
	}
	body, err := os.ReadFile(filepath.Join(repoDir, deployOverlayPath))
	if err != nil {
		return nil, false, err
	}
	return body, true, nil
}

func deployYAMLForCheckout(app AppConfig) ([]byte, string, error) {
	generated, err := GeneratedDeployYAML(app)
	if err != nil {
		return nil, "", err
	}
	overlay, ok, err := repoDeployOverlay(app.RepoDir)
	if err != nil {
		return nil, "", err
	}
	if !ok {
		return generated, generatedDeployConfigLabel, nil
	}
	merged, err := mergeDeployOverlay(generated, overlay)
	if err != nil {
		return nil, "", err
	}
	return merged, deployOverlayConfigSource, nil
}

func cliMergeDeployConfig(args []string) error {
	if len(args) != 2 {
		return fmt.Errorf("usage: singleserver %s <overlay> <output>", mergeDeployConfigCommand)
	}
	generated, ok := os.LookupEnv(generatedDeployYAMLEnv)
	if !ok {
		return errors.New(generatedDeployYAMLEnv + " is not set")
	}
	overlay, err := os.ReadFile(args[0])
	if err != nil {
		return err
	}
	merged, err := mergeDeployOverlay([]byte(generated), overlay)
	if err != nil {
		return err
	}
	return os.WriteFile(args[1], merged, 0644)
}
