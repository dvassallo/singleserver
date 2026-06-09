package singleserver

import (
	"encoding/json"
	"fmt"
	"strings"
)

type GeneratedDockerfileFiles struct {
	Dockerfile  string
	NginxConfig string
	Source      string
}

func (a AppConfig) UsesGeneratedDockerfile() bool {
	return strings.TrimSpace(a.Runtime) != ""
}

func GeneratedDockerfile(app AppConfig) (GeneratedDockerfileFiles, error) {
	if strings.TrimSpace(app.Runtime) == "" {
		return GeneratedDockerfileFiles{}, nil
	}
	if err := app.Normalize(); err != nil {
		return GeneratedDockerfileFiles{}, err
	}

	staticOutput := app.Runtime == "static" || app.StaticDir != ""
	switch {
	case app.Runtime == "static":
		return generatedStaticDockerfile(app, "source", "alpine:3.20", "", "")
	case staticOutput:
		return generatedStaticDockerfile(app, "build", runtimeBuilderImage(app.Runtime), app.InstallCommand, app.BuildCommand)
	default:
		return generatedDynamicDockerfile(app)
	}
}

func generatedStaticDockerfile(app AppConfig, stageName, builderImage, installCommand, buildCommand string) (GeneratedDockerfileFiles, error) {
	nginxConfig, err := generatedNginxConfig(app.HealthcheckPath)
	if err != nil {
		return GeneratedDockerfileFiles{}, err
	}

	var b strings.Builder
	fmt.Fprintf(&b, "FROM %s AS %s\n", builderImage, stageName)
	b.WriteString("WORKDIR /app\n")
	b.WriteString("COPY . .\n")
	if installCommand != "" {
		fmt.Fprintf(&b, "RUN %s\n", installCommand)
	}
	if buildCommand != "" {
		fmt.Fprintf(&b, "RUN %s\n", buildCommand)
	}
	b.WriteString("RUN rm -rf .git .kamal .singleserver .singleserver-generated\n\n")
	b.WriteString("FROM nginx:1.27-alpine\n")
	fmt.Fprintf(&b, "RUN printf '%%b' %s > /etc/nginx/conf.d/default.conf\n", shellPrintfBQuote(nginxConfig))
	fmt.Fprintf(&b, "COPY --from=%s %s /usr/share/nginx/html/\n", stageName, dockerStaticSource(app.StaticDir))
	b.WriteString("EXPOSE 80\n")

	return GeneratedDockerfileFiles{
		Dockerfile:  b.String(),
		NginxConfig: nginxConfig,
		Source:      "generated:" + app.Runtime,
	}, nil
}

func generatedDynamicDockerfile(app AppConfig) (GeneratedDockerfileFiles, error) {
	var b strings.Builder
	fmt.Fprintf(&b, "FROM %s\n", runtimeBuilderImage(app.Runtime))
	b.WriteString("WORKDIR /app\n")
	b.WriteString("COPY . .\n")
	if app.InstallCommand != "" {
		fmt.Fprintf(&b, "RUN %s\n", app.InstallCommand)
	}
	if app.BuildCommand != "" {
		fmt.Fprintf(&b, "RUN %s\n", app.BuildCommand)
	}
	b.WriteString("RUN rm -rf .git .kamal .singleserver .singleserver-generated\n")
	if app.Runtime == "node" {
		b.WriteString("ENV NODE_ENV=production\n")
	}
	fmt.Fprintf(&b, "ENV PORT=%d\n", app.AppPort)
	fmt.Fprintf(&b, "EXPOSE %d\n", app.AppPort)
	command, err := shellCommandJSON(app.StartCommand)
	if err != nil {
		return GeneratedDockerfileFiles{}, err
	}
	fmt.Fprintf(&b, "CMD [\"sh\", \"-lc\", %s]\n", command)

	return GeneratedDockerfileFiles{
		Dockerfile: b.String(),
		Source:     "generated:" + app.Runtime,
	}, nil
}

func runtimeBuilderImage(runtime string) string {
	switch runtime {
	case "bun":
		return "oven/bun:1-alpine"
	default:
		return "node:20-alpine"
	}
}

func dockerStaticSource(staticDir string) string {
	if staticDir == "." {
		return "/app/"
	}
	return "/app/" + staticDir + "/"
}

func shellCommandJSON(command string) (string, error) {
	body, err := json.Marshal(command)
	if err != nil {
		return "", err
	}
	return string(body), nil
}

func shellPrintfBQuote(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, "\n", `\n`)
	value = strings.ReplaceAll(value, `'`, `'"'"'`)
	return "'" + value + "'"
}

func generatedNginxConfig(healthcheckPath string) (string, error) {
	if err := validateNginxLocationPath(healthcheckPath); err != nil {
		return "", err
	}
	healthcheckLocation := ""
	if healthcheckPath != "/" {
		healthcheckLocation = fmt.Sprintf(`
    location = %s {
        access_log off;
        add_header Content-Type text/plain;
        return 200 "OK\n";
    }
`, healthcheckPath)
	}
	return fmt.Sprintf(`server {
    listen 80 default_server;
    server_name _;
    root /usr/share/nginx/html;
    index index.html index.htm;
%s

    location ~ /\.(?!well-known) {
        deny all;
    }

    location / {
        add_header Cache-Control "no-cache";
        try_files $uri $uri/ /index.html =404;
    }
}
`, healthcheckLocation), nil
}

func validateNginxLocationPath(value string) error {
	if strings.TrimSpace(value) == "" || !strings.HasPrefix(value, "/") {
		return fmt.Errorf("healthcheck_path must start with /")
	}
	if strings.ContainsAny(value, " \t\r\n{};\"'\\") {
		return fmt.Errorf("healthcheck_path is not safe for generated nginx config: %q", value)
	}
	return nil
}
