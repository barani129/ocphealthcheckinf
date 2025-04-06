/*
Copyright 2025 baranitharan.chittharanjan@spark.co.nz.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

	http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package hubutil

import (
	"context"
	"fmt"
	"time"

	ocpscanv1 "github.com/barani129/ocphealthcheckinf/api/v1"
	ocphealthcheckutil "github.com/barani129/ocphealthcheckinf/internal/util"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	ocmpolicy "open-cluster-management.io/governance-policy-propagator/api/v1"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

func OnPolicyUpdate(staticClientSet *kubernetes.Clientset, spec *ocpscanv1.OcpHealthCheckSpec, runningHost string) {
	policies := ocmpolicy.PolicyList{}
	err := staticClientSet.RESTClient().Get().AbsPath("/apis/policy.open-cluster-management.io/v1/policies").Do(context.Background()).Into(&policies)
	if err != nil {
		return
	}
	for _, policy := range policies.Items {
		if !ocphealthcheckutil.IgnoredPolicy(spec, &policy) {
			if policy.DeletionTimestamp != nil {
				// assuming it is deletion, so will ignore it
				return
			}
			// alerts for policies that are non-compliant within last 6 hours
			pastTime := metav1.Now().Add(-1 * time.Hour * 6)
			if !ocphealthcheckutil.IsChildPolicy(&policy) {
				if policy.Status.ComplianceState == ocphealthcheckutil.POLICYNONCOMPLIANT || policy.Spec.Disabled {
					if policy.Status.Details != nil {
						for _, detail := range policy.Status.Details {
							if detail.History != nil {
								if !detail.History[0].LastTimestamp.Time.Before(pastTime) {
									ocphealthcheckutil.SendEmail("Policy-update", fmt.Sprintf("/home/golanguser/files/ocphealth/.%s-%s.txt", policy.Name, policy.Namespace), "faulty", fmt.Sprintf("Root policy %s is either non-compliant/disabled in namespace %s in cluster %s, please execute <oc get policy %s -n %s -o json | jq .status> to validate it", policy.Name, policy.Namespace, runningHost, policy.Name, policy.Namespace), runningHost, spec)
								} else {
									log.Log.Info(fmt.Sprintf("Ignoring the non-compliant policy %s in namespace %s as it is non-compliant for more than 6 hours", policy.Name, policy.Namespace))
								}
							}
						}
					}
				} else {
					ocphealthcheckutil.SendEmail("Policy-update", fmt.Sprintf("/home/golanguser/files/ocphealth/.%s-%s.txt", policy.Name, policy.Namespace), "recovered", fmt.Sprintf("Root policy %s which was previously non-compliant/disabled is now compliant/enabled again in namespace %s in cluster %s", policy.Name, policy.Namespace, runningHost), runningHost, spec)
				}
			}
		} else {
			log.Log.Info(fmt.Sprintf("Ignoring policy %s's update in namespace %s as it is configured to be ignored", policy.Name, policy.Namespace))
		}
	}
}
