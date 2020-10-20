package v1

import (
	"github.com/arangodb/kube-arangodb/pkg/apis/shared"
	sharedv1 "github.com/arangodb/kube-arangodb/pkg/apis/shared/v1"
	"github.com/pkg/errors"
	core "k8s.io/api/core/v1"
)

const (
	ServerGroupReservedInitContainerNameLifecycle = "init-lifecycle"
	ServerGroupReservedInitContainerNameUUID      = "uuid"
)

func IsReservedServerGroupInitContainerName(name string) bool {
	switch name {
	case ServerGroupReservedInitContainerNameLifecycle, ServerGroupReservedInitContainerNameUUID:
		return true
	default:
		return false
	}
}

func ValidateServerGroupInitContainerName(name string) error {
	if IsReservedServerGroupInitContainerName(name) {
		return errors.Errorf("InitContainer name %s is restricted", name)
	}

	return sharedv1.AsKubernetesResourceName(&name).Validate()
}

type ServerGroupInitContainerMode string

func (s *ServerGroupInitContainerMode) Get() ServerGroupInitContainerMode {
	if s == nil {
		return ServerGroupInitContainerUpdateMode // default
	}

	return *s
}

func (s ServerGroupInitContainerMode) New() *ServerGroupInitContainerMode {
	return &s
}

func (s *ServerGroupInitContainerMode) Validate() error {
	switch v := s.Get(); v {
	case ServerGroupInitContainerIgnoreMode, ServerGroupInitContainerUpdateMode:
		return nil
	default:
		return errors.Errorf("Unknown serverGroupInitContainerMode %s", v)
	}
}

const (
	// ServerGroupInitContainerIgnoreMode ignores init container changes in pod recreation flow
	ServerGroupInitContainerIgnoreMode ServerGroupInitContainerMode = "ignore"
	// ServerGroupInitContainerUpdateMode enforce update of pod if init container has been changed
	ServerGroupInitContainerUpdateMode ServerGroupInitContainerMode = "update"
)

type ServerGroupInitContainers struct {
	// Containers contains list of containers
	Containers []core.Container `json:"containers,omitempty"`

	// Mode keep container replace mode
	Mode *ServerGroupInitContainerMode `json:"mode,omitempty"`
}

func (s *ServerGroupInitContainers) GetMode() *ServerGroupInitContainerMode {
	if s == nil {
		return nil
	}

	return s.Mode
}

func (s *ServerGroupInitContainers) GetContainers() []core.Container {
	if s == nil {
		return nil
	}

	return s.Containers
}

func (s *ServerGroupInitContainers) Validate() error {
	if s == nil {
		return nil
	}

	return shared.WithErrors(
		shared.PrefixResourceError("mode", s.Mode.Validate()),
		shared.PrefixResourceError("containers", s.validateInitContainers()),
	)
}

func (s *ServerGroupInitContainers) validateInitContainers() error {
	for _, c := range s.Containers {
		if err := ValidateServerGroupInitContainerName(c.Name); err != nil {
			return err
		}
	}

	return nil
}
