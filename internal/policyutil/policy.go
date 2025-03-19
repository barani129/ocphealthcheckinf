package policyutil

import (
	"context"
	"fmt"

	ocphealthcheckv1 "github.com/barani129/ocphealthcheckinf/api/v1"
	ocphealthcheckutil "github.com/barani129/ocphealthcheckinf/internal/util"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	ocmpolicy "open-cluster-management.io/governance-policy-propagator/api/v1"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

func OnPolicyAdd(newObj interface{}, spec *ocphealthcheckv1.OcpHealthCheckSpec, status *ocphealthcheckv1.OcpHealthCheckStatus) {
	policy := new(ocmpolicy.Policy)
	err := ocphealthcheckutil.ConvertUnStructureToStructured(newObj, policy)
	if err != nil {
		log.Log.Error(err, "failed to convert")
		return
	}
	if !IsChildPolicy(policy) {
		log.Log.Info(fmt.Sprintf("New policy.open-cluster-management.io/v1 %s has been added to namespace %s", policy.Name, policy.Namespace))
	} else {
		log.Log.Info(fmt.Sprintf("New child policy %s has been added to namespace %s, possible CGU update is in progress, please check HUB cluster", policy.Name, policy.Namespace))
	}
}

func OnPolicyUpdate(newObj interface{}, spec *ocphealthcheckv1.OcpHealthCheckSpec, status *ocphealthcheckv1.OcpHealthCheckStatus, runningHost string) {
	policy := new(ocmpolicy.Policy)
	err := ocphealthcheckutil.ConvertUnStructureToStructured(newObj, policy)
	if err != nil {
		log.Log.Error(err, "failed to convert")
		return
	}
	if policy.DeletionTimestamp != nil {
		// assuming it is deletion, so will ignore it
		return
	}
	if !IsChildPolicy(policy) {
		if policy.Status.ComplianceState == "NonCompliant" || policy.Spec.Disabled {
			ocphealthcheckutil.SendEmail(fmt.Sprintf("/home/golanguser/files/.%s-%s.txt", policy.Name, "noncomplaint"), "faulty", fmt.Sprintf("policy %s is either non-compliant/disabled in namespace %s in cluster %s, please execute <oc get policy %s -n %s -o json | jq .status> to validate it", policy.Name, policy.Namespace, runningHost, policy.Name, policy.Namespace), runningHost, spec)
		} else {
			ocphealthcheckutil.SendEmail(fmt.Sprintf("/home/golanguser/files/.%s-%s.txt", policy.Name, "noncomplaint"), "recovered", fmt.Sprintf("policy %s which was previously non-compliant/disabled is now compliant/enabled again in namespace %s in cluster %s", policy.Name, policy.Namespace, runningHost), runningHost, spec)
		}
	}
}

func OnPolicyDelete(oldObj interface{}, spec *ocphealthcheckv1.OcpHealthCheckSpec, status *ocphealthcheckv1.OcpHealthCheckStatus, runningHost string) {
	policy := new(ocmpolicy.Policy)
	err := ocphealthcheckutil.ConvertUnStructureToStructured(oldObj, policy)
	if err != nil {
		log.Log.Error(err, "failed to convert")
		return
	}
	ocphealthcheckutil.SendEmail(fmt.Sprintf("/home/golanguser/files/.%s-%s.txt", policy.Name, "noncomplaint"), "recovered", fmt.Sprintf("policy %s which was previously non-compliant/disabled is now deleted in namespace %s in cluster %s", policy.Name, policy.Namespace, runningHost), runningHost, spec)
}

func IsChildPolicy(policy *ocmpolicy.Policy) bool {
	for labelName, _ := range policy.Labels {
		if labelName == "openshift-cluster-group-upgrades/parentPolicyName" {
			return true
		}
	}
	return false
}

func IsChildPolicyNamespace(clientset *kubernetes.Clientset, ns string) (bool, error) {
	policyNamespace, err := GetChildPolicyObjectNamespace(clientset)
	if err != nil {
		return false, err
	}
	if policyNamespace != "" && policyNamespace == ns {
		return true, nil
	}
	return false, nil
}

func GetChildPolicyObjectNamespace(clientset *kubernetes.Clientset) (string, error) {
	policies := ocmpolicy.PolicyList{}
	err := clientset.RESTClient().Get().AbsPath("/apis/policy.open-cluster-management.io/v1/policies").Do(context.Background()).Into(&policies)
	if err != nil {
		return "", err
	}
	if len(policies.Items) > 0 {
		for _, policy := range policies.Items {
			if IsChildPolicy(&policy) {
				if policy.Name == "ztp-cwl.storage-netapp-trident" {
					for _, temp := range policy.Spec.PolicyTemplates {
						obj, _, err := unstructured.UnstructuredJSONScheme.Decode(temp.ObjectDefinition.Raw, nil, nil)
						if err != nil {
							return "", err
						}

						unObj, err := runtime.DefaultUnstructuredConverter.ToUnstructured(obj)
						if err != nil {
							return "", err
						}
						var objNamespace string
						for _, val := range unObj {
							if v2, ok := val.(map[string]interface{}); ok {
								for k3, v3 := range v2 {
									if k3 == "object-templates" {
										for _, v4 := range v3.([]interface{}) {
											if v5, ok := v4.(map[string]interface{}); ok {
												for k6, v6 := range v5 {
													if k6 == "objectDefinition" {
														for k7, v7 := range v6.(map[string]interface{}) {
															if k7 == "metadata" {
																for k8, v8 := range v7.(map[string]interface{}) {
																	if k8 == "namespace" {
																		if v8.(string) != "" {
																			objNamespace = v8.(string)
																			return objNamespace, nil
																		}
																	}
																}
															}
														}
													}
												}
											}
										}
									}
								}
							}
						}
					}
				}
			}
		}
	}
	return "", nil
}
