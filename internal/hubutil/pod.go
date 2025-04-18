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
	"os"
	"strings"
	"sync"

	ocpscanv1 "github.com/barani129/ocphealthcheckinf/api/v1"
	ocphealthcheckutil "github.com/barani129/ocphealthcheckinf/internal/util"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

func OnPodUpdate(clientset *kubernetes.Clientset, spec *ocpscanv1.OcpHealthCheckSpec, runningHost string) {
	if mcp, err := ocphealthcheckutil.CheckMCPINProgress(clientset); err != nil {
		log.Log.Error(err, "unable to retrieve mcp")
		return
	} else if err == nil && mcp {
		log.Log.Info("MCP in progress")
		return
	}
	var wg sync.WaitGroup
	nodeList, err := clientset.CoreV1().Nodes().List(context.Background(), metav1.ListOptions{})
	if err != nil {
		log.Log.Error(err, "unable to receive node list")
		return
	}

	wg.Add(len(nodeList.Items))
	for _, node := range nodeList.Items {
		go func() {
			defer wg.Done()
			fieldSelector := fmt.Sprintf(`spec.nodeName=%s`, node.Name)
			podList, err := clientset.CoreV1().Pods(corev1.NamespaceAll).List(context.Background(), metav1.ListOptions{
				FieldSelector: fieldSelector,
			})
			if err != nil {
				log.Log.Error(err, "unable to retrieve pod list")
				return
			}
			for _, newPo := range podList.Items {
				// ignoring resources that are configured to be ignored
				if !ocphealthcheckutil.IgnoredPod(spec, &newPo) {
					// ignoring pod changes during node restart
					if nodeAffected, err := ocphealthcheckutil.CheckSingleNodeReadiness(clientset, newPo.Spec.NodeName); err != nil {
						log.Log.Info("unable to retrieve node information")
						return
					} else if nodeAffected {
						log.Log.Info("Exiting as node is not-ready/unschedulable")
						return
					}
					if sameNs, err := ocphealthcheckutil.IsChildPolicyNamespace(clientset, newPo.Namespace); err != nil {
						log.Log.Info("unable to retrieve policy object namespace")
						return
					} else if sameNs {
						log.Log.Info("Exiting as child policy update is in progress")
						// ocphealthcheckutil.SendEmail("Pod", fmt.Sprintf("cgu update in progress for namespace %s", newPo.Namespace), "faulty", fmt.Sprintf("possible CGU update is in progress for objects in namespace %s in cluster %s, no pod update alerts will be sent until CGU is compliant, please execute <oc get pods -n %s and oc get policy -A> to validate", newPo.Namespace, runningHost, newPo.Namespace), runningHost, spec)
						return
					}
					if newPo.DeletionTimestamp == nil {
						if newPo.Status.InitContainerStatuses != nil {
							for _, newCont := range newPo.Status.InitContainerStatuses {
								ocphealthcheckutil.PodCheck(clientset, newPo, newCont, spec, runningHost)
							}
						}
						for _, newCont := range newPo.Status.ContainerStatuses {
							ocphealthcheckutil.PodCheck(clientset, newPo, newCont, spec, runningHost)
						}
					} else {
						files, err := os.ReadDir("/home/golanguser/files/ocphealth/")
						if err != nil {
							return
						}
						for _, file := range files {
							if strings.Contains(file.Name(), newPo.Name) && strings.Contains(file.Name(), newPo.Namespace) {
								ocphealthcheckutil.SendEmail("Pod", fmt.Sprintf("/home/golanguser/files/ocphealth/%s", file.Name()), "recovered", fmt.Sprintf("pod %s's container which was previously terminated/CrashLoopBackOff is now deleted in namespace %s in cluster %s ", newPo.Name, newPo.Namespace, runningHost), runningHost, spec)
							}
						}
					}
				} else {
					log.Log.Info(fmt.Sprintf("Ignoring pod %s's update in namespace %s as it is configured to be ignored", newPo.Name, newPo.Namespace))
				}
			}
		}()
	}
	wg.Wait()
}
