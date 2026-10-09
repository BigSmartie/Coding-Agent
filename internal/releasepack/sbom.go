package releasepack

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"time"
)

type Module struct {
	Path    string  `json:"Path"`
	Version string  `json:"Version"`
	Main    bool    `json:"Main"`
	Replace *Module `json:"Replace,omitempty"`
}

func LocalModules() ([]Module, error) {
	command := exec.Command("go", "list", "-m", "-json", "all")
	output, err := command.Output()
	if err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(output))
	var modules []Module
	for {
		var module Module
		if err := decoder.Decode(&module); err != nil {
			if err == io.EOF {
				break
			}
			return nil, err
		}
		modules = append(modules, module)
	}
	return modules, nil
}

func SBOM(modules []Module, version string, epoch int64) ([]byte, error) {
	if !safeVersion.MatchString(version) || epoch <= 0 {
		return nil, fmt.Errorf("invalid SBOM version or epoch")
	}
	packages := []map[string]any{}
	relationships := []map[string]string{}
	for i, module := range modules {
		if module.Path == "" {
			return nil, fmt.Errorf("module has no path")
		}
		id := fmt.Sprintf("SPDXRef-Package-%d", i+1)
		moduleVersion := module.Version
		if module.Main {
			moduleVersion = version
		}
		if module.Replace != nil && module.Replace.Version != "" {
			moduleVersion = module.Replace.Version
		}
		if moduleVersion == "" {
			moduleVersion = "NOASSERTION"
		}
		purl := "pkg:golang/" + module.Path
		if moduleVersion != "NOASSERTION" {
			purl += "@" + moduleVersion
		}
		packages = append(packages, map[string]any{
			"SPDXID": id, "name": module.Path, "versionInfo": moduleVersion, "downloadLocation": "NOASSERTION", "filesAnalyzed": false,
			"licenseConcluded": "NOASSERTION", "copyrightText": "NOASSERTION",
			"externalRefs": []map[string]string{{"referenceCategory": "PACKAGE-MANAGER", "referenceType": "purl", "referenceLocator": purl}},
		})
		relationships = append(relationships, map[string]string{"spdxElementId": "SPDXRef-DOCUMENT", "relatedSpdxElement": id, "relationshipType": "DESCRIBES"})
	}
	document := map[string]any{
		"spdxVersion": "SPDX-2.3", "dataLicense": "CC0-1.0", "SPDXID": "SPDXRef-DOCUMENT", "name": "mythoscode-" + version,
		"documentNamespace": "https://github.com/BigSmartie/Coding-Agent/spdxdocs/mythoscode-" + version,
		"creationInfo":      map[string]any{"creators": []string{"Tool: mythoscode-releasepack"}, "created": time.Unix(epoch, 0).UTC().Format("2006-01-02T15:04:05Z")},
		"packages":          packages, "relationships": relationships,
	}
	data, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

func WriteSBOM(path, version string, epoch int64) error {
	modules, err := LocalModules()
	if err != nil {
		return err
	}
	data, err := SBOM(modules, version, epoch)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}
