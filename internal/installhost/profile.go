//go:build linux

package installhost

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"strings"
)

const (
	HostProfileAuto          = "auto"
	HostProfileArchSystemd   = "arch-systemd"
	HostProfileDebianSystemd = "debian-systemd"
)

func ResolveSystemProfile(selection string) (SystemProfile, error) {
	switch selection {
	case HostProfileArchSystemd:
		return ArchSystemdProfile(), nil
	case HostProfileDebianSystemd:
		return DebianSystemdProfile(), nil
	case HostProfileAuto:
		content, err := os.ReadFile("/etc/os-release")
		if err != nil {
			return SystemProfile{}, fmt.Errorf("detect host profile: %w", err)
		}
		return DetectSystemProfile(content)
	default:
		return SystemProfile{}, fmt.Errorf("unsupported host profile %q", selection)
	}
}

func DetectSystemProfile(osRelease []byte) (SystemProfile, error) {
	values := map[string]string{}
	scanner := bufio.NewScanner(strings.NewReader(string(osRelease)))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, found := strings.Cut(line, "=")
		if !found {
			continue
		}
		values[key] = strings.Trim(strings.TrimSpace(value), `"'`)
	}
	if err := scanner.Err(); err != nil {
		return SystemProfile{}, err
	}
	switch values["ID"] {
	case "arch":
		return ArchSystemdProfile(), nil
	case "debian":
		return DebianSystemdProfile(), nil
	default:
		return SystemProfile{}, errors.New(
			"supported host profile could not be detected from /etc/os-release",
		)
	}
}

func ProfileForSpecification(
	name string,
	trustProfile string,
) (SystemProfile, error) {
	if name == "" {
		if trustProfile == TrustProfileP11Kit {
			name = HostProfileArchSystemd
		} else if trustProfile == TrustProfileDebianCA {
			name = HostProfileDebianSystemd
		}
	}
	profile, err := ResolveSystemProfile(name)
	if err != nil {
		return SystemProfile{}, err
	}
	if profile.TrustProfile != trustProfile {
		return SystemProfile{}, errors.New(
			"installation host profile does not match its trust profile",
		)
	}
	return profile, nil
}
