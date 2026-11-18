package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"openfox/internal/runtime"
	"openfox/internal/runtime/cliagent"
	"openfox/internal/runtime/codex"
	"openfox/internal/runtime/hermes"
)

type runtimeProbeResponse struct {
	Capabilities runtime.Capabilities `json:"capabilities"`
	Error        string               `json:"error,omitempty"`
}

type stringListFlag []string

func (f *stringListFlag) String() string { return strings.Join(*f, ",") }

func (f *stringListFlag) Set(value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return errors.New("runtime: supported version cannot be empty")
	}
	*f = append(*f, value)
	return nil
}

func runRuntime(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: openfox runtime <probe>")
	}
	switch args[0] {
	case "probe":
		return runRuntimeProbe(args[1:])
	default:
		return fmt.Errorf("unknown runtime command %q", args[0])
	}
}

func runRuntimeProbe(args []string) error {
	flags := flag.NewFlagSet("runtime probe", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	executable := flags.String("executable", "", "Runtime executable or PATH name (empty: auto-discover)")
	provider := flags.String("provider", "codex", "Runtime provider (codex, codebuddy, hermes or gemini)")
	timeout := flags.Duration("timeout", 5*time.Second, "capability probe timeout")
	var supported stringListFlag
	flags.Var(&supported, "supported-version", "supported Codex version; may be repeated")
	jsonOutput := flags.Bool("json", false, "emit a JSON report")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected arguments: %v", flags.Args())
	}
	if *timeout <= 0 || *timeout > 30*time.Second {
		return errors.New("runtime probe timeout must be between 1ns and 30s")
	}
	if *provider == "codex" && len(supported) == 0 {
		if value := getenv("OPENFOX_CODEX_SUPPORTED_VERSIONS"); value != "" {
			for _, version := range strings.Split(value, ",") {
				if err := supported.Set(version); err != nil {
					return err
				}
			}
		}
	}
	var response runtimeProbeResponse
	if *provider == "codebuddy" {
		if len(supported) != 0 {
			return errors.New("CodeBuddy supports only fixed version 2.184.0; --supported-version is not accepted")
		}
		a := cliagent.NewCodeBuddy(*executable)
		a.ProbeTimeout = *timeout
		cap, err := a.Detect(context.Background())
		response.Capabilities = cap
		if err != nil {
			response.Error = err.Error()
		}
	} else if *provider == "gemini" {
		if len(supported) != 0 {
			return errors.New("Gemini supports only fixed CLI 0.67.0 and Node 22.23.1; --supported-version is not accepted")
		}
		a := cliagent.NewGemini(*executable)
		a.ProbeTimeout = *timeout
		cap, err := a.Detect(context.Background())
		response.Capabilities = cap
		if err != nil {
			response.Error = err.Error()
		}
	} else if *provider == "hermes" {
		if len(supported) != 0 || *executable != "" {
			return errors.New("Hermes uses fixed verified OPENFOX_HERMES_ROOT and OPENFOX_HERMES_PYTHON; executable/version overrides are not accepted")
		}
		a := hermes.New()
		a.ProbeTimeout = *timeout
		cap, err := a.Detect(context.Background())
		response.Capabilities = cap
		if err != nil {
			response.Error = err.Error()
		}
	} else if *provider == "codex" {
		response = probeRuntime(context.Background(), *executable, supported, *timeout)
	} else {
		return errors.New("runtime probe provider is not supported")
	}
	if *jsonOutput {
		encoded, err := json.Marshal(response)
		if err != nil {
			return err
		}
		fmt.Println(string(encoded))
		return nil
	}
	capabilities := response.Capabilities
	fmt.Printf("runtime=%s version=%s executable=%s login=%s runnable=%t\n",
		capabilities.Runtime, capabilities.Version, capabilities.Executable, capabilities.Login, capabilities.Runnable)
	if capabilities.Reason != "" {
		fmt.Printf("reason=%s\n", capabilities.Reason)
	}
	if response.Error != "" {
		fmt.Printf("error=%s\n", response.Error)
	}
	return nil
}

func probeRuntime(ctx context.Context, executable string, supported []string, timeout time.Duration) runtimeProbeResponse {
	adapter := codex.New(executable)
	adapter.SupportedVersions = append([]string(nil), supported...)
	adapter.ProbeTimeout = timeout
	report, err := adapter.Detect(ctx)
	if report.Runtime == "" {
		report.Runtime = codex.RuntimeName
	}
	if report.Login == "" {
		report.Login = runtime.LoginUnknown
	}
	if err != nil && report.Reason == "" {
		report.Reason = "runtime capability probe failed"
	}
	response := runtimeProbeResponse{Capabilities: report}
	if err != nil {
		response.Error = safeRuntimeError(err)
		if response.Capabilities.Reason == "" {
			response.Capabilities.Reason = "runtime capability probe failed"
		}
	}
	return response
}

func safeRuntimeError(err error) string {
	if err == nil {
		return ""
	}
	message := strings.Join(strings.Fields(err.Error()), " ")
	if len(message) > 512 {
		message = message[:512]
	}
	return message
}

func getenv(name string) string {
	return strings.TrimSpace(strings.ReplaceAll(os.Getenv(name), "\x00", ""))
}
