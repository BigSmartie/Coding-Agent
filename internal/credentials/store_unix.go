//go:build !windows

package credentials

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/BigSmartie/Coding-Agent/internal/workspace"
)

func helperPath(name, cwd string) (string, error) {
	path, err := exec.LookPath(name)
	if err != nil {
		return "", errors.New("OS credential helper not installed")
	}
	path, err = workspace.Canonical(path)
	if err != nil {
		return "", errors.New("cannot resolve OS credential helper")
	}
	root, err := workspace.Canonical(cwd)
	if err != nil {
		return "", err
	}
	if workspace.Within(root, path) {
		return "", errors.New("OS credential helper must be outside the workspace")
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 {
		return "", errors.New("OS credential helper must be a regular executable")
	}
	return path, nil
}

func helperEnv(environ []string) []string {
	env := []string{"PATH=/usr/bin:/bin:/usr/sbin:/sbin"}
	for _, entry := range environ {
		key, _, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		switch key {
		case "HOME", "USER", "LOGNAME", "DBUS_SESSION_BUS_ADDRESS", "XDG_RUNTIME_DIR", "DISPLAY", "WAYLAND_DISPLAY", "LANG", "LC_CTYPE":
			env = append(env, entry)
		}
	}
	return env
}

type boundedOutput struct {
	bytes.Buffer
	cancel context.CancelFunc
}

func (b *boundedOutput) Write(p []byte) (int, error) {
	if b.Len()+len(p) > 16*1024 {
		b.cancel()
		return 0, errors.New("credential helper output exceeds 16 KiB")
	}
	return b.Buffer.Write(p)
}

func runStore(name string, args []string, input string) (string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	path, err := helperPath(name, cwd)
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Env = helperEnv(os.Environ())
	cmd.Stdin = strings.NewReader(input)
	var output boundedOutput
	output.cancel = cancel
	cmd.Stdout = &output
	cmd.Stderr = io.Discard
	err = cmd.Run()
	if err != nil {
		return "", errors.New("OS credential store unavailable or operation denied; install/unlock Keychain or Secret Service (secret-tool); no plaintext fallback is available")
	}
	return strings.TrimRight(output.String(), "\r\n"), nil
}

func (SystemStore) Get(id string) (string, error) {
	if err := valid(id, "placeholder"); err != nil {
		return "", err
	}
	if runtime.GOOS == "darwin" {
		return runStore("/usr/bin/security", []string{"find-generic-password", "-s", "mythoscode", "-a", id, "-w"}, "")
	}
	if runtime.GOOS == "linux" {
		return runStore("secret-tool", []string{"lookup", "service", "mythoscode", "account", id}, "")
	}
	return "", errors.New("OS credential storage is unsupported on this platform")
}

func securityQuote(value string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(value) + `"`
}

func (SystemStore) Set(id, secret string) error {
	if err := valid(id, secret); err != nil {
		return err
	}
	var err error
	switch runtime.GOOS {
	case "darwin":
		// Interactive mode reads the secret from stdin, keeping it out of argv.
		_, err = runStore("/usr/bin/security", []string{"-i"}, "add-generic-password -U -s mythoscode -a "+securityQuote(id)+" -w "+securityQuote(secret)+"\n")
		if err == nil {
			// security -i may exit successfully after an individual command fails.
			stored, readErr := (SystemStore{}).Get(id)
			if readErr != nil || stored != secret {
				return errors.New("Keychain did not save the credential")
			}
		}
	case "linux":
		_, err = runStore("secret-tool", []string{"store", "--label=MythosCode provider credential", "service", "mythoscode", "account", id}, secret)
	default:
		err = errors.New("OS credential storage is unsupported on this platform")
	}
	return err
}

func (SystemStore) Delete(id string) error {
	if err := valid(id, "placeholder"); err != nil {
		return err
	}
	if runtime.GOOS == "darwin" {
		_, err := runStore("/usr/bin/security", []string{"delete-generic-password", "-s", "mythoscode", "-a", id}, "")
		return err
	}
	if runtime.GOOS == "linux" {
		_, err := runStore("secret-tool", []string{"clear", "service", "mythoscode", "account", id}, "")
		return err
	}
	return errors.New("OS credential storage is unsupported on this platform")
}
