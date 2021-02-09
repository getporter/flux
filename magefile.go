// +build mage

// This is a magefile, and is a "makefile for go".
// See https://magefile.org/
package main

import (
	"bytes"
	"fmt"
	"io/ioutil"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"text/template"

	"github.com/carolynvs/magex/mgx"
	"github.com/carolynvs/magex/pkg"
	"github.com/carolynvs/magex/shx"
	"github.com/magefile/mage/mg"
	"github.com/pkg/errors"
)

// Default target to run when none is specified
// If not set, running mage will list available targets
// var Default = Build

const (
	// Version of KIND to install if not already present
	kindVersion = "v0.10.0"

	// Name of the KIND cluster used for testing
	kindClusterName = "porter"

	// Namespace where you can do manual testing
	testNamespace = "test"

	// Relative location of the KUBECONFIG for the test cluster
	kubeconfig = "kind.config"

	// Namespace of the porter operator
	operatorNamespace = "porter-operator-system"

	// Container name of the local registry
	registryContainer = "registry"
)

// Build a command that stops the build on if the command fails
var must = shx.CommandBuilder{StopOnError: true}

// Ensure mage is installed.
func EnsureMage() error {
	addGopathBinOnGithubActions()
	return pkg.EnsureMage("v1.11.0")
}

// Add GOPATH/bin to the path on the GitHub Actions agent
// TODO: Add to magex
func addGopathBinOnGithubActions() error {
	githubPath := os.Getenv("GITHUB_PATH")
	if githubPath == "" {
		return nil
	}

	log.Println("Adding GOPATH/bin to the PATH for the GitHub Actions Agent")
	gopathBin := pkg.GetGopathBin()
	return ioutil.WriteFile(githubPath, []byte(gopathBin), 0644)
}

func Generate() {
	must.RunV("controller-gen", `object:headerFile="hack/boilerplate.go.txt"`, `paths="./..."`)
}

func Fmt() {
	must.RunV("go", "fmt", "./...")
}

func Vet() {
	must.RunV("go", "vet", "./...")
}

// Run all tests
func Test() {
	mg.Deps(TestUnit)
}

// Run unit tests.
func TestUnit() {
	must.RunV("go", "test", "./...", "-coverprofile", "coverage-unit.out")
}

// Ensure operator-sdk is installed.
func EnsureOperatorSDK() {
	const version = "v1.3.0"

	if runtime.GOOS == "windows" {
		mgx.Must(errors.New("Sorry, OperatorSDK does not support Windows. In order to contribute to this repository, you will need to use WSL."))
	}

	url := "https://github.com/operator-framework/operator-sdk/releases/{{.VERSION}}/download/operator-sdk_{{.GOOS}}_{{.GOARCH}}"
	mgx.Must(pkg.DownloadToGopathBin(url, "operator-sdk", version))
}

// Ensure that the test KIND cluster is up.
func EnsureCluster() {
	mg.Deps(EnsureKubectl)

	if !useCluster() {
		CreateKindCluster()
	}
	configureCluster()
}

// get the config of the current kind cluster, if available
func getClusterConfig() (kubeconfig string, ok bool) {
	contents, err := shx.OutputE("kind", "get", "kubeconfig", "--name", kindClusterName)
	return contents, err == nil
}

// setup environment to use the current kind cluster, if available
func useCluster() bool {
	contents, ok := getClusterConfig()
	if ok {
		log.Println("Reusing existing kind cluster")

		userKubeConfig, _ := filepath.Abs(os.Getenv("KUBECONFIG"))
		currentKubeConfig := filepath.Join(pwd(), kubeconfig)
		if userKubeConfig != currentKubeConfig {
			fmt.Printf("ATTENTION! You should set your KUBECONFIG to match the cluster used by this project\n\n\texport KUBECONFIG=%s\n\n", currentKubeConfig)
		}
		os.Setenv("KUBECONFIG", currentKubeConfig)

		err := ioutil.WriteFile(kubeconfig, []byte(contents), 0644)
		mgx.Must(errors.Wrapf(err, "error writing %s", kubeconfig))

		setClusterNamespace(operatorNamespace)
		return true
	}

	return false
}

func setClusterNamespace(name string) {
	must.RunE("kubectl", "config", "set-context", "--current", "--namespace", name)
}

// Create a KIND cluster named porter.
func CreateKindCluster() {
	mg.Deps(EnsureKind)

	// Determine host ip to populate kind config api server details
	// https://kind.sigs.k8s.io/docs/user/configuration/#api-server
	addrs, err := net.InterfaceAddrs()
	mgx.Must(errors.Wrap(err, "could not get a list of network interfaces"))

	var ipAddress string
	for _, address := range addrs {
		if ipnet, ok := address.(*net.IPNet); ok && !ipnet.IP.IsLoopback() {
			if ipnet.IP.To4() != nil {
				fmt.Println("Current IP address : ", ipnet.IP.String())
				ipAddress = ipnet.IP.String()
				break
			}
		}
	}

	os.Setenv("KUBECONFIG", filepath.Join(pwd(), kubeconfig))
	kindCfg, err := ioutil.ReadFile("hack/kind.config.yaml")
	mgx.Must(errors.Wrap(err, "error reading hack/kind.config.yaml"))

	kindCfgTmpl, err := template.New("kind.config.yaml").Parse(string(kindCfg))
	mgx.Must(errors.Wrap(err, "error parsing Kind config template hack/kind.config.yaml"))

	var kindCfgContents bytes.Buffer
	kindCfgData := struct {
		Address string
	}{
		Address: ipAddress,
	}
	err = kindCfgTmpl.Execute(&kindCfgContents, kindCfgData)
	err = ioutil.WriteFile("kind.config.yaml", kindCfgContents.Bytes(), 0644)
	mgx.Must(errors.Wrap(err, "could not write kind config file"))
	defer os.Remove("kind.config.yaml")

	must.Run("kind", "create", "cluster", "--name", kindClusterName, "--config", "kind.config.yaml")

	// Connect the kind and registry containers on the same network
	must.Run("docker", "network", "connect", "kind", registryContainer)

	// Document the local registry
	kubectl("apply", "-f", "hack/local-registry.yaml").Run()
}

func configureCluster() {
	mg.Deps(StartDockerRegistry)

	setClusterNamespace(operatorNamespace)

	must.RunV("flux", "install")
}

// Delete the KIND cluster named porter.
func DeleteKindCluster() {
	mg.Deps(EnsureKind)

	must.RunE("kind", "delete", "cluster", "--name", kindClusterName)

	if isOnDockerNetwork(registryContainer, "kind") {
		must.RunE("docker", "network", "disconnect", "kind", registryContainer)
	}
}

func isOnDockerNetwork(container string, network string) bool {
	networkId, _ := shx.OutputE("docker", "network", "inspect", network, "-f", "{{.Id}}")
	networks, _ := shx.OutputE("docker", "inspect", container, "-f", "{{json .NetworkSettings.Networks}}")
	return strings.Contains(networks, networkId)
}

// Ensure kind is installed.
func EnsureKind() {
	if ok, _ := pkg.IsCommandAvailable("kind", ""); ok {
		return
	}

	kindURL := "https://github.com/kubernetes-sigs/kind/releases/download/{{.VERSION}}/kind-{{.GOOS}}-{{.GOARCH}}"
	mgx.Must(pkg.DownloadToGopathBin(kindURL, "kind", kindVersion))
}

// Ensure kubectl is installed.
func EnsureKubectl() {
	if ok, _ := pkg.IsCommandAvailable("kubectl", ""); ok {
		return
	}

	versionURL := "https://storage.googleapis.com/kubernetes-release/release/stable.txt"
	versionResp, err := http.Get(versionURL)
	mgx.Must(errors.Wrapf(err, "unable to determine the latest version of kubectl"))

	if versionResp.StatusCode > 299 {
		mgx.Must(errors.Errorf("GET %s (%s): %s", versionURL, versionResp.StatusCode, versionResp.Status))
	}
	defer versionResp.Body.Close()

	kubectlVersion, err := ioutil.ReadAll(versionResp.Body)
	mgx.Must(errors.Wrapf(err, "error reading response from %s", versionURL))

	kindURL := "https://storage.googleapis.com/kubernetes-release/release/{{.VERSION}}/bin/{{.GOOS}}/{{.GOARCH}}/kubectl{{.EXT}}"
	mgx.Must(pkg.DownloadToGopathBin(kindURL, "kubectl", string(kubectlVersion)))
}

// Run a makefile target
func makefile(args ...string) shx.PreparedCommand {
	cmd := must.Command("make", args...)
	cmd.Env("KUBECONFIG=" + os.Getenv("KUBECONFIG"))

	return cmd
}

func kubectl(args ...string) shx.PreparedCommand {
	kubeconfig := fmt.Sprintf("KUBECONFIG=%s", os.Getenv("KUBECONFIG"))
	return must.Command("kubectl", args...).Env(kubeconfig)
}

func kustomize(args ...string) shx.PreparedCommand {
	cmd := filepath.Join(pwd(), "bin/kustomize")
	return must.Command(cmd, args...)
}

// Ensure yq is installed.
func EnsureYq() {
	mgx.Must(pkg.EnsurePackage("github.com/mikefarah/yq/v4", "", ""))
}

// Ensure ginkgo is installed.
func EnsureGinkgo() {
	mgx.Must(pkg.EnsurePackage("github.com/onsi/ginkgo/ginkgo", "", ""))
}

// Ensure kustomize is installed.
func EnsureKustomize() {
	// TODO: implement installing from a URL that is tgz
	makefile("kustomize").Run()
}

func EnsureFlux() {
	// TODO
}

// Ensure controller-gen is installed.
func EnsureControllerGen() {
	mgx.Must(pkg.EnsurePackage("sigs.k8s.io/controller-tools/cmd/controller-gen", "v0.4.1", "--version"))
}

// Ensure that a local docker registry is running.
func StartDockerRegistry() {
	if isContainerRunning(registryContainer) {
		return
	}

	StopDockerRegistry()

	fmt.Println("Starting local docker registry")
	must.RunE("docker", "run", "-d", "-p", "5000:5000", "--name", registryContainer, "registry:2")
}

// Stops the local docker registry.
func StopDockerRegistry() {
	if containerExists(registryContainer) {
		fmt.Println("Stopping local docker registry")
		removeContainer(registryContainer)
	}
}

func isContainerRunning(name string) bool {
	out, _ := shx.OutputS("docker", "container", "inspect", "-f", "{{.State.Running}}", name)
	running, _ := strconv.ParseBool(out)
	return running
}

func containerExists(name string) bool {
	err := shx.RunS("docker", "inspect", name)
	return err == nil
}

func removeContainer(name string) {
	stderr, err := shx.OutputE("docker", "rm", "-f", name)
	// Gracefully handle the container already being gone
	if err != nil && !strings.Contains(stderr, "No such container") {
		mgx.Must(err)
	}
}

func pwd() string {
	wd, _ := os.Getwd()
	return wd
}
