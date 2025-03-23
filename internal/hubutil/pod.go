package hubutil

import (
	"context"
	"fmt"
	"sync"

	ocpscanv1 "github.com/barani129/ocphealthcheckinf/api/v1"
	ocphealthcheckutil "github.com/barani129/ocphealthcheckinf/internal/util"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
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
					for _, newCont := range newPo.Status.ContainerStatuses {
						if newCont.State.Terminated != nil && newCont.State.Terminated.ExitCode != 0 {
							ocphealthcheckutil.SendEmail("Pod", fmt.Sprintf("/home/golanguser/files/ocphealth/.%s-%s.txt", newPo.Name, newPo.Namespace), "faulty", fmt.Sprintf("pod %s's container %s is terminated with non exit code 0 in namespace %s in cluster %s", newPo.Name, newCont.Name, newPo.Namespace, runningHost), runningHost, spec)
						} else if newCont.State.Running != nil || (newCont.State.Terminated != nil && newCont.State.Terminated.ExitCode == 0) {
							// Assuming if pod has moved back to running from CrashLoopBackOff/others, the restart count will always be greater than 0
							if newCont.RestartCount > 0 {
								if newCont.LastTerminationState.Terminated != nil && newCont.LastTerminationState.Terminated.ExitCode != 0 && newCont.LastTerminationState.Terminated.FinishedAt.String() != "" {
									if ocphealthcheckutil.PodLastRestartTimerUp(newCont.LastTerminationState.Terminated.FinishedAt.String()) {
										ocphealthcheckutil.SendEmail("Pod", fmt.Sprintf("/home/golanguser/files/ocphealth/.%s-%s.txt", newPo.Name, newPo.Namespace), "faulty", fmt.Sprintf("pod %s's container %s whic was previously terminate with non exit code 0 is now either running/completed in namespace %s in cluster %s", newPo.Name, newCont.Name, newPo.Namespace, runningHost), runningHost, spec)
									}
								}
							}
						}
						if newCont.State.Waiting != nil {
							if newCont.State.Waiting.Reason == "CrashLoopBackOff" {
								if len(newPo.Spec.Volumes) > 0 {
									for _, vol := range newPo.Spec.Volumes {
										if vol.PersistentVolumeClaim != nil && vol.PersistentVolumeClaim.ClaimName != "" {
											// Get the persistent volume claim name
											pvc, err := clientset.CoreV1().PersistentVolumeClaims(newPo.Namespace).Get(context.Background(), vol.PersistentVolumeClaim.ClaimName, metav1.GetOptions{})
											if err != nil {
												if k8serrors.IsNotFound(err) {
													ocphealthcheckutil.SendEmail("Pod", fmt.Sprintf("/home/golanguser/files/ocphealth/.%s-%s.txt", newPo.Name, newPo.Namespace), "faulty", fmt.Sprintf("Pod %s's container %s is in CrashLoopBackOff state, configured PVC %s doesn't exist in namespace %s in cluster %s, please execute <oc get pods %s -n %s -o json | jq .spec.volumes[] and oc get pvc %s -n %s -o json | jq .spec.volumeName> to validate it", newPo.Name, newCont.Name, pvc.Name, pvc.Namespace, runningHost, newPo.Name, newPo.Namespace, pvc.Name, pvc.Namespace), runningHost, spec)
												} else {
													ocphealthcheckutil.SendEmail("Pod", fmt.Sprintf("/home/golanguser/files/ocphealth/.%s-%s.txt", newPo.Name, newPo.Namespace), "faulty", fmt.Sprintf("Pod %s's container %s is in CrashLoopBackOff state, unable to retrieve configured PVC %s in namespace %s in cluster %s, please execute <oc get pods %s -n %s -o json | jq .spec.volumes[] and oc get pvc %s -n %s -o json | jq .spec.volumeName> to validate it", newPo.Name, newCont.Name, pvc.Name, pvc.Namespace, runningHost, newPo.Name, newPo.Namespace, pvc.Name, pvc.Namespace), runningHost, spec)
												}
											}
											if affected, err := ocphealthcheckutil.PvHasDifferentNode(clientset, pvc.Spec.VolumeName, newPo.Spec.NodeName); err != nil {
												ocphealthcheckutil.SendEmail("Pod", fmt.Sprintf("/home/golanguser/files/ocphealth/.%s-%s.txt", newPo.Name, newPo.Namespace), "faulty", fmt.Sprintf("Pod %s's container %s is in CrashLoopBackOff state, unable to retrieve volume attachment of volume %s in cluster %s, please execute <oc get pods %s -n %s -o json | jq .status.containerStatuses and oc get volumeattachments | grep %s > to validate it", newPo.Name, newCont.Name, pvc.Spec.VolumeName, runningHost, newPo.Name, newPo.Namespace, pvc.Spec.VolumeName), runningHost, spec)
											} else if err == nil && affected {
												ocphealthcheckutil.SendEmail("Pod", fmt.Sprintf("/home/golanguser/files/ocphealth/.%s-%s.txt", newPo.Name, newPo.Namespace), "faulty", fmt.Sprintf("Pod %s's container %s is in CrashLoopBackOff state, volume attachment of volume %s is mounted on a different node in cluster %s, please execute <oc get pods %s -n %s -o json | jq .status.containerStatuses and oc get volumeattachments | grep %s > to validate it", newPo.Name, newCont.Name, pvc.Spec.VolumeName, runningHost, newPo.Name, newPo.Namespace, pvc.Spec.VolumeName), runningHost, spec)
											} else {
												// Check if it is due to other issues
												for _, cont := range newPo.Spec.Containers {
													if cont.Name == newCont.Name {
														if newCont.Image != cont.Image {
															ocphealthcheckutil.SendEmail("Pod", fmt.Sprintf("/home/golanguser/files/ocphealth/.%s-%s.txt", newPo.Name, newPo.Namespace), "faulty", fmt.Sprintf("Pod %s's container %s is in CrashLoopBackOff state, volume attachment of volume %s is mounted on the SAME node, could be other issues in cluster %s, please execute <oc get pods %s -n %s -o json | jq .status.containerStatuses and oc get volumeattachments | grep %s > to validate it", newPo.Name, newCont.Name, pvc.Spec.VolumeName, runningHost, newPo.Name, newPo.Namespace, pvc.Spec.VolumeName), runningHost, spec)
														}
													}
												}
											}
										} else {
											for _, cont := range newPo.Spec.Containers {
												if cont.Name == newCont.Name {
													if newCont.Image != cont.Image {
														ocphealthcheckutil.SendEmail("Pod", fmt.Sprintf("/home/golanguser/files/ocphealth/.%s-%s.txt", newPo.Name, newPo.Namespace), "faulty", fmt.Sprintf("Pod %s's container %s is in CrashLoopBackOff state, appears to be ErrImagePull error in cluster %s, please execute <oc get pods %s -n %s -o json | jq .status.containerStatuses and oc  > to validate it", newPo.Name, newCont.Name, runningHost, newPo.Name, newPo.Namespace), runningHost, spec)
													} else {
														ocphealthcheckutil.SendEmail("Pod", fmt.Sprintf("/home/golanguser/files/ocphealth/.%s-%s.txt", newPo.Name, newPo.Namespace), "faulty", fmt.Sprintf("Pod %s's container %s is in CrashLoopBackOff state, no persistent volume is attached to the pod, doesn't seem to be ErrImagePull, could be other issues, in cluster %s, please execute <oc get pods %s -n %s -o json | jq .status.containerStatuses> to validate it", newPo.Name, newCont.Name, runningHost, newPo.Name, newPo.Namespace), runningHost, spec)
													}
												}
											}
										}
									}
								}
							} else if newCont.State.Waiting.Reason == ocphealthcheckutil.PODERRIMAGEPULL || newCont.State.Waiting.Reason == ocphealthcheckutil.PODIMAGEPULLBACKOFF {
								ocphealthcheckutil.SendEmail("Pod", fmt.Sprintf("/home/golanguser/files/ocphealth/.%s-%s.txt", newPo.Name, newPo.Namespace), "faulty", fmt.Sprintf("pod %s's container %s is failing in namespace %s due to ErrImagePull in cluster %s", newPo.Name, newCont.Name, newPo.Namespace, runningHost), runningHost, spec)
							}
						} else {
							// Assuming if pod has moved back to running from CrashLoopBackOff/others, the restart count will always be greater than 0
							if newCont.RestartCount > 0 {
								if newCont.LastTerminationState.Terminated != nil && newCont.LastTerminationState.Terminated.ExitCode != 0 && newCont.LastTerminationState.Terminated.FinishedAt.String() != "" {
									if ocphealthcheckutil.PodLastRestartTimerUp(newCont.LastTerminationState.Terminated.FinishedAt.String()) {
										ocphealthcheckutil.SendEmail("Pod", fmt.Sprintf("/home/golanguser/files/ocphealth/.%s-%s.txt", newPo.Name, newPo.Namespace), "recovered", fmt.Sprintf("pod %s's container %s which was previously terminated with non exit code 0 is now either running/completed in namespace %s in cluster %s ", newPo.Name, newCont.Name, newPo.Namespace, runningHost), runningHost, spec)
									}
								} else if newCont.State.Terminated != nil && newCont.State.Terminated.ExitCode == 0 {
									// this condition will satisfy the pod that was previously running/completed and went into issues (due to image pull for example) and becomes running/completed
									ocphealthcheckutil.SendEmail("Pod", fmt.Sprintf("/home/golanguser/files/ocphealth/.%s-%s.txt", newPo.Name, newPo.Namespace), "recovered", fmt.Sprintf("pod %s's container %s which was previously terminated with non exit code 0 is now either running/completed in namespace %s in cluster %s ", newPo.Name, newCont.Name, newPo.Namespace, runningHost), runningHost, spec)
								}
							} else {
								// this condition will satisfy the pod that was never in running state (due to image pull for example) and becomes running/completed
								if newCont.State.Terminated == nil {
									ocphealthcheckutil.SendEmail("Pod", fmt.Sprintf("/home/golanguser/files/ocphealth/.%s-%s.txt", newPo.Name, newPo.Namespace), "recovered", fmt.Sprintf("pod %s's container %s which was previously terminated/waiting is now either running/completed in namespace %s in cluster %s ", newPo.Name, newCont.Name, newPo.Namespace, runningHost), runningHost, spec)
								}
							}
						}
					}
				} else {
					ocphealthcheckutil.SendEmail("Pod", fmt.Sprintf("/home/golanguser/files/ocphealth/.%s-%s.txt", newPo.Name, newPo.Namespace), "recovered", fmt.Sprintf("pod %s's container which was previously terminated/CrashLoopBackOff is now being deleted in namespace %s in cluster %s ", newPo.Name, newPo.Namespace, runningHost), runningHost, spec)
				}
			}
		}()
	}
	wg.Wait()
}
