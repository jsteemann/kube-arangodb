//
// DISCLAIMER
//
// Copyright 2018 ArangoDB GmbH, Cologne, Germany
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
//
// Copyright holder is ArangoDB GmbH, Cologne, Germany
//
// Author Adam Janikowski
//

package backup

import (
	"fmt"
	"reflect"
	"sync"
	"time"

	"github.com/arangodb/go-driver"
	"github.com/arangodb/kube-arangodb/pkg/backup/utils"
	"k8s.io/apimachinery/pkg/util/uuid"

	"github.com/arangodb/kube-arangodb/pkg/backup/operator"

	"github.com/arangodb/kube-arangodb/pkg/backup/operator/event"
	"github.com/arangodb/kube-arangodb/pkg/backup/operator/operation"

	"k8s.io/client-go/kubernetes"

	"k8s.io/apimachinery/pkg/api/errors"

	"github.com/rs/zerolog/log"

	backupApi "github.com/arangodb/kube-arangodb/pkg/apis/backup/v1alpha"
	database "github.com/arangodb/kube-arangodb/pkg/apis/deployment/v1alpha"
	arangoClientSet "github.com/arangodb/kube-arangodb/pkg/generated/clientset/versioned"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	defaultArangoClientTimeout = 30 * time.Second

	// StateChange name of the event send when state changed
	StateChange = "StateChange"

	// FinalizerChange name of the event send when finalizer removed entry
	FinalizerChange = "FinalizerChange"
)

type handler struct {
	lock  sync.Mutex
	locks map[string]*sync.Mutex

	client     arangoClientSet.Interface
	kubeClient kubernetes.Interface

	eventRecorder event.RecorderInstance

	arangoClientFactory ArangoClientFactory
	arangoClientTimeout time.Duration

	operator operator.Operator
}

func (h *handler) Start(stopCh <-chan struct{}) {
	go h.start(stopCh)
}

func (h *handler) start(stopCh <-chan struct{}) {
	t := time.NewTicker(2 * time.Minute)
	defer t.Stop()

	for {
		select {
		case <-stopCh:
			return
		case <-t.C:
			log.Debug().Msgf("Refreshing database objects")
			if err := h.refresh(); err != nil {
				log.Error().Err(err).Msgf("Unable to refresh database objects")
			}
			log.Debug().Msgf("Database objects refreshed")
		}
	}
}

func (h *handler) refresh() error {
	deployments, err := h.client.DatabaseV1alpha().ArangoDeployments(h.operator.Namespace()).List(meta.ListOptions{})
	if err != nil {
		return err
	}

	for _, deployment := range deployments.Items {
		if err = h.refreshDeployment(&deployment); err != nil {
			return err
		}
	}

	return nil
}

func (h *handler) refreshDeployment(deployment *database.ArangoDeployment) error {
	m := h.getDeploymentMutex(deployment.Namespace, deployment.Name)
	m.Lock()
	defer m.Unlock()

	client, err := h.arangoClientFactory(deployment, nil)
	if err != nil {
		return err
	}

	backups, err := h.client.BackupV1alpha().ArangoBackups(deployment.Namespace).List(meta.ListOptions{})
	if err != nil {
		return err
	}

	existingBackups, err := client.List()
	if err != nil {
		return err
	}

	for _, backupMeta := range existingBackups {
		if err = h.refreshDeploymentBackup(deployment, backupMeta, backups.Items); err != nil {
			return err
		}
	}

	return nil
}

func (h *handler) refreshDeploymentBackup(deployment *database.ArangoDeployment, backupMeta driver.BackupMeta, backups []backupApi.ArangoBackup) error {
	for _, backup := range backups {
		if download := backup.Spec.Download; download != nil {
			if download.ID == string(backupMeta.ID) {
				return nil
			}
		}

		if backup.Status.Backup == nil {
			continue
		}

		if backup.Status.Backup.ID == string(backupMeta.ID) {
			return nil
		}
	}

	// New backup found, need to recreate
	backup := &backupApi.ArangoBackup{
		ObjectMeta: meta.ObjectMeta{
			Name:      fmt.Sprintf("backup-%s", uuid.NewUUID()),
			Namespace: deployment.Namespace,
		},
		Spec: backupApi.ArangoBackupSpec{
			Deployment: backupApi.ArangoBackupSpecDeployment{
				Name: deployment.Name,
			},
		},
	}

	_, err := h.client.BackupV1alpha().ArangoBackups(backup.Namespace).Create(backup)
	if err != nil {
		return err
	}

	trueVar := true

	backup.Status = backupApi.ArangoBackupStatus{
		Backup: &backupApi.ArangoBackupDetails{
			ID:                string(backupMeta.ID),
			Version:           backupMeta.Version,
			CreationTimestamp: meta.Now(),
			Imported:          &trueVar,
		},
		Available: true,
		ArangoBackupState: backupApi.ArangoBackupState{
			Time:  meta.Now(),
			State: backupApi.ArangoBackupStateReady,
		},
	}

	err = h.updateBackupStatus(backup)
	if err != nil {
		return err
	}

	return nil
}

func (h *handler) Name() string {
	return backupApi.ArangoBackupResourceKind
}

func (h *handler) updateBackupStatus(b *backupApi.ArangoBackup) error {
	return utils.Retry(25, time.Second, func() error {
		backup, err := h.client.BackupV1alpha().ArangoBackups(b.Namespace).Get(b.Name, meta.GetOptions{})
		if err != nil {
			return err
		}

		backup.Status = b.Status

		_, err = h.client.BackupV1alpha().ArangoBackups(b.Namespace).UpdateStatus(backup)
		return err
	})
}

func (h *handler) getDeploymentMutex(namespace, deployment string) *sync.Mutex {
	h.lock.Lock()
	defer h.lock.Unlock()

	if h.locks == nil {
		h.locks = map[string]*sync.Mutex{}
	}

	name := fmt.Sprintf("%s/%s", namespace, deployment)

	if _, ok := h.locks[name]; !ok {
		h.locks[name] = &sync.Mutex{}
	}

	return h.locks[name]
}

func (h *handler) Handle(item operation.Item) error {
	// Get Backup object. It also cover NotFound case
	b, err := h.client.BackupV1alpha().ArangoBackups(item.Namespace).Get(item.Name, meta.GetOptions{})
	if err != nil {
		if errors.IsNotFound(err) {
			return nil
		}

		return err
	}

	// Check if we should start finalizer
	if b.DeletionTimestamp != nil {
		log.Debug().Msgf("Finalizing %s %s/%s",
			item.Kind,
			item.Namespace,
			item.Name)

		return h.finalize(b)
	}

	// Add finalizers
	if !hasFinalizers(b) {
		b.Finalizers = appendFinalizers(b)
		log.Info().Msgf("Updating finalizers %s %s/%s",
			item.Kind,
			item.Namespace,
			item.Name)

		if _, err = h.client.BackupV1alpha().ArangoBackups(item.Namespace).Update(b); err != nil {
			return err
		}

		return nil
	}

	// Create lock per namespace to ensure that we are not using 2 goroutines in same time
	lock := h.getDeploymentMutex(b.Namespace, b.Spec.Deployment.Name)
	lock.Lock()
	defer lock.Unlock()

	// Add owner reference
	if b.OwnerReferences == nil || len(b.OwnerReferences) == 0 {
		deployment, err := h.client.DatabaseV1alpha().ArangoDeployments(b.Namespace).Get(b.Spec.Deployment.Name, meta.GetOptions{})
		if err == nil {
			b.OwnerReferences = []meta.OwnerReference{
				deployment.AsOwner(),
			}

			if _, err = h.client.BackupV1alpha().ArangoBackups(item.Namespace).Update(b); err != nil {
				return err
			}
		}
	}

	status, err := h.processArangoBackup(b.DeepCopy())
	if err != nil {
		log.Warn().Err(err).Msgf("Fail for %s %s/%s",
			item.Kind,
			item.Namespace,
			item.Name)
		return err
	}

	status.Time = b.Status.Time

	// Nothing to update, objects are equal
	if reflect.DeepEqual(b.Status, status) {
		return nil
	}

	if h.operator != nil {
		h.operator.EnqueueItem(item)
	}

	// Ensure that transit is possible
	if err = backupApi.ArangoBackupStateMap.Transit(b.Status.State, status.State); err != nil {
		return err
	}

	// Log message about state change
	if b.Status.State != status.State {
		status.Time = meta.Now()
		if status.State == backupApi.ArangoBackupStateFailed {
			h.eventRecorder.Warning(b, StateChange, "Transiting from %s to %s with error: %s",
				b.Status.State,
				status.State,
				status.Message)
		} else {
			h.eventRecorder.Normal(b, StateChange, "Transiting from %s to %s",
				b.Status.State,
				status.State)
		}
	}

	b.Status = status

	log.Debug().Msgf("Updating %s %s/%s",
		item.Kind,
		item.Namespace,
		item.Name)

	// Update status on object
	if err := h.updateBackupStatus(b); err != nil {
		return err
	}

	return nil
}

func (h *handler) processArangoBackup(backup *backupApi.ArangoBackup) (backupApi.ArangoBackupStatus, error) {
	if err := backup.Validate(); err != nil {
		return createFailedState(err, backup.Status), nil
	}

	if f, ok := stateHolders[backup.Status.State]; ok {
		return f(h, backup)
	}

	return backupApi.ArangoBackupStatus{}, fmt.Errorf("state %s is not supported", backup.Status.State)
}

func (h *handler) CanBeHandled(item operation.Item) bool {
	return item.Group == database.SchemeGroupVersion.Group &&
		item.Version == database.SchemeGroupVersion.Version &&
		item.Kind == backupApi.ArangoBackupResourceKind
}

func (h *handler) getArangoDeploymentObject(backup *backupApi.ArangoBackup) (*database.ArangoDeployment, error) {
	if backup.Spec.Deployment.Name == "" {
		return nil, fmt.Errorf("deployment ref is not specified for backup %s/%s", backup.Namespace, backup.Name)
	}

	return h.client.DatabaseV1alpha().ArangoDeployments(backup.Namespace).Get(backup.Spec.Deployment.Name, meta.GetOptions{})
}