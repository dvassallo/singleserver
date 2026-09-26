package singleserver

import (
	"bytes"
	"runtime"

	"gopkg.in/yaml.v3"
)

type kamalDeployConfig struct {
	Service       string                    `yaml:"service"`
	Image         string                    `yaml:"image"`
	Servers       map[string]kamalServer    `yaml:"servers"`
	SSH           kamalSSH                  `yaml:"ssh"`
	Registry      kamalRegistry             `yaml:"registry"`
	Builder       kamalBuilder              `yaml:"builder"`
	Proxy         kamalProxy                `yaml:"proxy"`
	Env           *kamalEnv                 `yaml:"env,omitempty"`
	Volumes       []string                  `yaml:"volumes,omitempty"`
	Accessories   map[string]kamalAccessory `yaml:"accessories,omitempty"`
	Logging       kamalLogging              `yaml:"logging"`
	DeployTimeout int                       `yaml:"deploy_timeout"`
	DrainTimeout  int                       `yaml:"drain_timeout"`
}

type kamalServer struct {
	Hosts   []string           `yaml:"hosts"`
	Options kamalServerOptions `yaml:"options"`
}

type kamalServerOptions struct {
	Init        bool `yaml:"init"`
	StopTimeout int  `yaml:"stop-timeout"`
}

type kamalSSH struct {
	User string   `yaml:"user"`
	Keys []string `yaml:"keys"`
}

type kamalRegistry struct {
	Server   string `yaml:"server"`
	Username string `yaml:"username"`
	Password string `yaml:"password"`
}

type kamalBuilder struct {
	Arch    string `yaml:"arch"`
	Local   bool   `yaml:"local"`
	Driver  string `yaml:"driver"`
	Context string `yaml:"context"`
}

type kamalProxy struct {
	Hosts          []string              `yaml:"hosts,omitempty"`
	AppPort        int                   `yaml:"app_port"`
	SSL            bool                  `yaml:"ssl"`
	ForwardHeaders bool                  `yaml:"forward_headers"`
	Healthcheck    kamalProxyHealthcheck `yaml:"healthcheck"`
	Run            kamalProxyRun         `yaml:"run"`
}

type kamalProxyRun struct {
	BindIPs []string `yaml:"bind_ips"`
}

type kamalProxyHealthcheck struct {
	Path     string `yaml:"path"`
	Interval int    `yaml:"interval"`
	Timeout  int    `yaml:"timeout"`
}

type kamalLogging struct {
	Driver  string              `yaml:"driver"`
	Options kamalLoggingOptions `yaml:"options"`
}

type kamalLoggingOptions struct {
	Tag string `yaml:"tag"`
}

type kamalEnv struct {
	Secret []string `yaml:"secret,omitempty"`
}

func GeneratedDeployYAML(app AppConfig) ([]byte, error) {
	if err := app.Normalize(); err != nil {
		return nil, err
	}

	config := kamalDeployConfig{
		Service: app.Name,
		Image:   app.Name,
		Servers: map[string]kamalServer{
			"web": {
				Hosts: []string{"127.0.0.1"},
				Options: kamalServerOptions{
					Init:        true,
					StopTimeout: app.StopTimeoutSeconds(),
				},
			},
		},
		SSH: kamalSSH{
			User: "deploy",
			Keys: []string{"/root/.ssh/id_ed25519"},
		},
		Registry: kamalRegistry{
			Server:   "127.0.0.1:5555",
			Username: "dummy",
			Password: "dummy",
		},
		Builder: kamalBuilder{
			Arch:    runtime.GOARCH,
			Local:   true,
			Driver:  "docker",
			Context: ".",
		},
		Proxy: kamalProxy{
			Hosts:          app.ProxyHosts(),
			AppPort:        app.AppPort,
			SSL:            false,
			ForwardHeaders: true,
			Healthcheck: kamalProxyHealthcheck{
				Path:     app.HealthcheckPath,
				Interval: 1,
				Timeout:  1,
			},
			Run: kamalProxyRun{
				BindIPs: []string{"127.0.0.1"},
			},
		},
		Logging: kamalLogging{
			Driver:  "journald",
			Options: kamalLoggingOptions{Tag: app.Name},
		},
		DeployTimeout: 10,
		DrainTimeout:  app.DrainTimeoutSeconds(),
	}
	if len(app.SecretEnvKeys) > 0 {
		config.Env = &kamalEnv{Secret: app.SecretEnvKeys}
	}
	if app.Storage != nil {
		config.Volumes = []string{app.Storage.Path + ":" + app.Storage.Mount}
	}
	if app.HasFunnel() {
		accessory, err := kamalFunnelAccessory(app)
		if err != nil {
			return nil, err
		}
		config.Accessories = map[string]kamalAccessory{funnelAccessory: accessory}
	}

	var buf bytes.Buffer
	encoder := yaml.NewEncoder(&buf)
	encoder.SetIndent(2)
	if err := encoder.Encode(config); err != nil {
		return nil, err
	}
	if err := encoder.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func appWithServerSecrets(app AppConfig) (AppConfig, error) {
	keys, err := appSecretKeys(app.Name)
	if err != nil {
		return AppConfig{}, err
	}
	app.SecretEnvKeys = keys
	return app, nil
}
