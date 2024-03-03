package app

import (
	"errors"
	"fmt"
	"os"
	"runtime"

	"github.com/hashicorp/terraform/command/cliconfig"
	"github.com/peterbourgon/ff/v4"
	"github.com/peterbourgon/ff/v4/ffhelp"
	"github.com/peterbourgon/ff/v4/ffyaml"
)

type config struct {
	Program     string
	MaxTasks    int
	PluginCache bool
	LogLevel    string
}

// set config in order of precedence:
// 1. flags > 2. env vars > 3. config file
func parse(args []string) (config, error) {
	var cfg config

	fs := ff.NewFlagSet("pug")
	fs.StringVar(&cfg.Program, 'p', "program", "terraform", "The default program to use with pug.")
	fs.IntVar(&cfg.MaxTasks, 't', "max-tasks", 2*runtime.NumCPU(), "The maximum number of parallel tasks.")
	fs.StringEnumVar(&cfg.LogLevel, 'l', "log-level", "Logging level.", "info", "debug", "error", "warn")
	_ = fs.String('c', "config", "pug.yaml", "Path to config file.")

	// Plugin cache is enabled not via pug but via terraform config
	tfcfg, _ := cliconfig.LoadConfig()
	cfg.PluginCache = (tfcfg.PluginCacheDir != "")

	err := ff.Parse(fs, args,
		ff.WithEnvVarPrefix("PUG"),
		ff.WithConfigFileFlag("config"),
		ff.WithConfigFileParser(ffyaml.Parse),
		ff.WithConfigAllowMissingFile(),
	)
	if errors.Is(err, ff.ErrHelp) {
		fmt.Fprintln(os.Stderr, ffhelp.Flags(fs))
		return config{}, nil
	}
	return cfg, err
}
