package singleserver

import (
	"fmt"
	"strings"
	"time"
)

var stopAppContainersFunc = stopAppContainers

func stopAppContainers(appName string) error {
	out, err := commandOutputFunc(5*time.Second, "docker", "ps", "-a", "--format", "{{.Names}}")
	if err != nil {
		return err
	}
	names := []string{}
	for _, name := range strings.Split(out, "\n") {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if strings.HasPrefix(name, appName+"-") || name == appName {
			names = append(names, name)
		}
	}
	if len(names) == 0 {
		return nil
	}
	args := append([]string{"rm", "-f"}, names...)
	return commandRunFunc(30*time.Second, "docker", args...)
}

func stopRunningAppContainers(appName string) ([]string, error) {
	out, err := commandOutputFunc(5*time.Second, "docker", "ps", "--format", "{{.Names}}")
	if err != nil {
		return nil, err
	}
	names := matchingAppContainerNames(appName, out)
	if len(names) == 0 {
		return nil, nil
	}
	args := append([]string{"stop"}, names...)
	if err := commandRunFunc(30*time.Second, "docker", args...); err != nil {
		return nil, err
	}
	return names, nil
}

func startContainers(names []string) error {
	if len(names) == 0 {
		return nil
	}
	args := append([]string{"start"}, names...)
	return commandRunFunc(30*time.Second, "docker", args...)
}

func appContainerName(appName string) (string, error) {
	out, err := commandOutputFunc(5*time.Second, "docker", "ps", "--format", "{{.Names}}")
	if err != nil {
		return "", err
	}
	containers := map[string]string{}
	for _, name := range strings.Split(out, "\n") {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		containers[name] = name
	}
	if name, ok := containerForApp(appName, containers); ok {
		return name, nil
	}
	return "", fmt.Errorf("no running container found for %s", appName)
}

func matchingAppContainerNames(appName string, output string) []string {
	names := []string{}
	for _, name := range strings.Split(output, "\n") {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if strings.HasPrefix(name, appName+"-") || name == appName {
			names = append(names, name)
		}
	}
	return names
}

func runningAppContainers() (map[string]string, error) {
	out, err := commandOutputFunc(5*time.Second, "docker", "ps", "--format", "{{.Names}}")
	if err != nil {
		return nil, err
	}
	containers := map[string]string{}
	for _, name := range strings.Split(out, "\n") {
		name = strings.TrimSpace(name)
		if name != "" {
			containers[name] = name
		}
	}
	return containers, nil
}

func containerForApp(appName string, containers map[string]string) (string, bool) {
	if containers == nil {
		return "", false
	}
	if container, ok := containers[appName]; ok {
		return container, true
	}
	for name, container := range containers {
		if strings.HasPrefix(name, appName+"-") {
			return container, true
		}
	}
	return "", false
}
