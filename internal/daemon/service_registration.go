package daemon

import (
	"encoding/xml"
	"flag"
	"io"
	"path/filepath"
)

// ForRegistration binds only the scoped desktop commands to their checked local
// registration. Validation runs inside the same lock as the service operation.
func (m Manager) ForRegistration(configPath string) Manager {
	m.registrationConfigPath = configPath
	return m
}

type servicePlistNode struct {
	XMLName  xml.Name
	Text     string             `xml:",chardata"`
	Children []servicePlistNode `xml:",any"`
}

type servicePathFlag struct {
	value string
	set   bool
}

func (v *servicePathFlag) String() string { return v.value }
func (v *servicePathFlag) Set(value string) error {
	// Go string flags normally keep the last value. Reject duplicates, including
	// repeated matching values, so an earlier matching pair can never authorize
	// a later overriding registration.
	if v.set {
		return ErrServiceUnavailable
	}
	v.value, v.set = value, true
	return nil
}

func (m Manager) validateServiceRegistration() error {
	path, exists, err := m.servicePath()
	if err != nil || !exists {
		return ErrServiceUnavailable
	}
	data, err := readPrivateFile(path, maxConfigBytes)
	if err != nil {
		return ErrServiceUnavailable
	}
	var root servicePlistNode
	if xml.Unmarshal(data, &root) != nil || root.XMLName.Local != "plist" || len(root.Children) != 1 || root.Children[0].XMLName.Local != "dict" {
		return ErrServiceUnavailable
	}
	values := root.Children[0].Children
	if len(values)%2 != 0 {
		return ErrServiceUnavailable
	}
	var arguments []string
	label := ""
	seen := map[string]bool{}
	for i := 0; i < len(values); i += 2 {
		key, value := values[i], values[i+1]
		if key.XMLName.Local != "key" || seen[key.Text] {
			return ErrServiceUnavailable
		}
		seen[key.Text] = true
		switch key.Text {
		case "ProgramArguments":
			if value.XMLName.Local != "array" {
				return ErrServiceUnavailable
			}
			for _, arg := range value.Children {
				if arg.XMLName.Local != "string" || len(arg.Children) != 0 {
					return ErrServiceUnavailable
				}
				arguments = append(arguments, arg.Text)
			}
		case "Label":
			if value.XMLName.Local != "string" {
				return ErrServiceUnavailable
			}
			label = value.Text
		case "Program":
			// The project-generated service uses ProgramArguments[0] as executable.
			// A different Program could interpret the remaining flags differently.
			return ErrServiceUnavailable
		}
	}
	if label != m.label() || len(arguments) < 3 || !filepath.IsAbs(arguments[0]) || arguments[1] != "daemon" || arguments[2] != "run" {
		return ErrServiceUnavailable
	}
	flags := flag.NewFlagSet("scoped daemon run", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	config, socket := &servicePathFlag{}, &servicePathFlag{}
	flags.Var(config, "config", "")
	flags.Var(socket, "socket", "")
	if flags.Parse(arguments[3:]) != nil || flags.NArg() != 0 || !config.set || config.value != m.registrationConfigPath {
		return ErrServiceUnavailable
	}
	// runDaemonRun derives the socket beside --config when not explicitly set.
	if !socket.set {
		socket.value = filepath.Join(filepath.Dir(config.value), "daemon.sock")
	}
	if socket.value != m.SocketPath {
		return ErrServiceUnavailable
	}
	return nil
}
