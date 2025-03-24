package hubutil

import (
	"context"
	"fmt"

	ocpscanv1 "github.com/barani129/ocphealthcheckinf/api/v1"
	ocphealthcheckutil "github.com/barani129/ocphealthcheckinf/internal/util"
	"k8s.io/client-go/kubernetes"
	ocmpolicy "open-cluster-management.io/governance-policy-propagator/api/v1"
)

func OnPolicyUpdate(staticClientSet *kubernetes.Clientset, spec *ocpscanv1.OcpHealthCheckSpec, runningHost string) {
	policies := ocmpolicy.PolicyList{}
	err := staticClientSet.RESTClient().Get().AbsPath("/apis/policy.open-cluster-management.io/v1/policies").Do(context.Background()).Into(&policies)
	if err != nil {
		return
	}
	for _, policy := range policies.Items {
		if policy.DeletionTimestamp != nil {
			// assuming it is deletion, so will ignore it
			return
		}
		if !ocphealthcheckutil.IsChildPolicy(&policy) {
			if policy.Status.ComplianceState == ocphealthcheckutil.POLICYNONCOMPLIANT || policy.Spec.Disabled {
				ocphealthcheckutil.SendEmail("Policy-update", fmt.Sprintf("/home/golanguser/files/ocphealth/.%s-%s.txt", policy.Name, policy.Namespace), "faulty", fmt.Sprintf("Root policy %s is either non-compliant/disabled in namespace %s in cluster %s, please execute <oc get policy %s -n %s -o json | jq .status> to validate it", policy.Name, policy.Namespace, runningHost, policy.Name, policy.Namespace), runningHost, spec)
			} else {
				ocphealthcheckutil.SendEmail("Policy-update", fmt.Sprintf("/home/golanguser/files/ocphealth/.%s-%s.txt", policy.Name, policy.Namespace), "recovered", fmt.Sprintf("Root policy %s which was previously non-compliant/disabled is now compliant/enabled again in namespace %s in cluster %s", policy.Name, policy.Namespace, runningHost), runningHost, spec)
			}
		}
	}
}
