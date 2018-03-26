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
// Author Ewout Prangsma
//

package reconcile

import "github.com/rs/zerolog"

// Reconciler is the service that takes care of bring the a deployment
// in line with its (changed) specification.
type Reconciler struct {
	log     zerolog.Logger
	context Context
}

// NewReconciler creates a new reconciler with given context.
func NewReconciler(log zerolog.Logger, context Context) *Reconciler {
	return &Reconciler{
		log:     log,
		context: context,
	}
}