package singleserver

import (
	"strings"
	"testing"
)

func TestGeneratedDockerfileEmptyWhenRuntimeUnset(t *testing.T) {
	files, err := GeneratedDockerfile(AppConfig{Repo: "acme/homepage"})
	if err != nil {
		t.Fatal(err)
	}
	if files.Dockerfile != "" || files.NginxConfig != "" || files.Source != "" {
		t.Fatalf("unexpected generated files: %#v", files)
	}
}

func TestGeneratedStaticDockerfile(t *testing.T) {
	files, err := GeneratedDockerfile(AppConfig{
		Repo:      "acme/homepage",
		Runtime:   "static",
		StaticDir: "dist",
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"FROM alpine:3.20 AS source",
		"FROM nginx:1.27-alpine",
		"COPY --from=source /app/dist/ /usr/share/nginx/html/",
		"EXPOSE 80",
	} {
		if !strings.Contains(files.Dockerfile, want) {
			t.Fatalf("generated Dockerfile missing %q:\n%s", want, files.Dockerfile)
		}
	}
	if !strings.Contains(files.NginxConfig, "location = /up") {
		t.Fatalf("generated nginx config missing healthcheck:\n%s", files.NginxConfig)
	}
	if files.Source != "generated:static" {
		t.Fatalf("unexpected source: %s", files.Source)
	}
}

func TestGeneratedNodeDynamicDockerfile(t *testing.T) {
	files, err := GeneratedDockerfile(AppConfig{
		Repo:           "acme/api",
		Runtime:        "node",
		InstallCommand: "npm ci",
		BuildCommand:   "npm run build",
		StartCommand:   "npm start",
		AppPort:        3000,
		AppPortSet:     true,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"FROM node:20-alpine",
		"RUN npm ci",
		"RUN npm run build",
		"ENV PORT=3000",
		"EXPOSE 3000",
		`CMD ["sh", "-lc", "npm start"]`,
	} {
		if !strings.Contains(files.Dockerfile, want) {
			t.Fatalf("generated Dockerfile missing %q:\n%s", want, files.Dockerfile)
		}
	}
	if files.NginxConfig != "" {
		t.Fatalf("dynamic app should not have nginx config:\n%s", files.NginxConfig)
	}
}

func TestGeneratedBunStaticBuildDockerfile(t *testing.T) {
	files, err := GeneratedDockerfile(AppConfig{
		Repo:           "acme/homepage",
		Runtime:        "bun",
		InstallCommand: "bun install --frozen-lockfile",
		BuildCommand:   "bun run build",
		StaticDir:      "public",
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"FROM oven/bun:1-alpine AS build",
		"RUN bun install --frozen-lockfile",
		"RUN bun run build",
		"FROM nginx:1.27-alpine",
		"COPY --from=build /app/public/ /usr/share/nginx/html/",
	} {
		if !strings.Contains(files.Dockerfile, want) {
			t.Fatalf("generated Dockerfile missing %q:\n%s", want, files.Dockerfile)
		}
	}
	if strings.Contains(files.Dockerfile, "CMD ") {
		t.Fatalf("static build should not include CMD:\n%s", files.Dockerfile)
	}
}

func TestGeneratedNginxConfigDoesNotOverrideRootHealthcheck(t *testing.T) {
	config, err := generatedNginxConfig("/")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(config, "location = / {") {
		t.Fatalf("root healthcheck should use the normal static route:\n%s", config)
	}
	if !strings.Contains(config, "location / {") {
		t.Fatalf("expected normal static route:\n%s", config)
	}
}
