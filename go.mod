module get.porter.sh/flux

go 1.15

require (
	github.com/carolynvs/magex v0.5.0
	github.com/fluxcd/pkg/runtime v0.6.2
	github.com/fluxcd/pkg/untar v0.0.5
	github.com/fluxcd/source-controller/api v0.6.1
	github.com/go-logr/logr v0.3.0
	github.com/magefile/mage v1.11.0
	github.com/pkg/errors v0.9.1
	github.com/spf13/pflag v1.0.5
	k8s.io/apimachinery v0.20.0
	k8s.io/client-go v0.20.0
	sigs.k8s.io/controller-runtime v0.7.0
)
