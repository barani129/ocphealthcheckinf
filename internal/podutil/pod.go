package podutil

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	ocphealthcheckv1 "github.com/barani129/ocphealthcheckinf/api/v1"
	"github.com/barani129/ocphealthcheckinf/internal/policyutil"
	ocphealthcheckutil "github.com/barani129/ocphealthcheckinf/internal/util"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

func OnPodDelete(oldObj interface{}, spec *ocphealthcheckv1.OcpHealthCheckSpec, status *ocphealthcheckv1.OcpHealthCheckStatus, runningHost string) {
	po := new(corev1.Pod)
	err := ocphealthcheckutil.ConvertUnStructureToStructured(oldObj, po)
	if err != nil {
		log.Log.Error(err, "failed to convert")
		return
	}
	ocphealthcheckutil.SendEmail(fmt.Sprintf("/home/golanguser/files/.%s-%s.txt", po.Name, po.Namespace), "recovered", fmt.Sprintf("pod %s's container which was previously terminated/CrashLoopBackOff is now deleted in namespace %s in cluster %s ", po.Name, po.Namespace, runningHost), runningHost, spec)
}

func OnPodAdd(oldObj interface{}) {
	po := new(corev1.Pod)
	err := ocphealthcheckutil.ConvertUnStructureToStructured(oldObj, po)
	if err != nil {
		log.Log.Error(err, "failed to convert")
		return
	}
	log.Log.Info(fmt.Sprintf("pod %s has been added to namespace %s", po.Name, po.Namespace))
}

func PodLastRestartTimerUp(timeStr string) bool {
	oldTime, err := time.Parse(time.RFC3339, timeStr)
	if err != nil {
		return false
	}
	var timePast bool
	currTime := time.Now().Add(-30 * time.Minute)
	timePast = oldTime.Before(currTime)
	return timePast
}

func CleanUpRunningPods(clientset *kubernetes.Clientset, spec *ocphealthcheckv1.OcpHealthCheckSpec, status *ocphealthcheckv1.OcpHealthCheckStatus, runningHost string) {
	podList, err := clientset.CoreV1().Pods(corev1.NamespaceAll).List(context.Background(), metav1.ListOptions{})
	if err != nil {
		log.Log.Info(fmt.Sprintf("unable to retrieve pods due to error %s", err.Error()))
	}
	if files, err := os.ReadDir("/home/golanguser/files/"); err != nil {
		log.Log.Info(err.Error())
	} else {
		for _, file := range files {
			if len(podList.Items) > 0 {
				for _, pod := range podList.Items {
					if strings.Contains(file.Name(), fmt.Sprintf(".%s-%s.txt", pod.Name, pod.Namespace)) {
						failingContainers := []string{}
						timersUp := []bool{}
						for _, cont := range pod.Status.ContainerStatuses {
							if cont.State.Running == nil {
								failingContainers = append(failingContainers, cont.Name)
							} else {
								if cont.RestartCount < 1 {
									if cont.LastTerminationState.Terminated != nil && cont.LastTerminationState.Terminated.ExitCode != 0 && cont.LastTerminationState.Terminated.FinishedAt.String() != "" {
										if !PodLastRestartTimerUp(cont.LastTerminationState.Terminated.FinishedAt.String()) {
											timersUp = append(timersUp, false)
										}
									}
								}

							}
						}
						if len(failingContainers) < 1 && len(timersUp) < 1 {
							ocphealthcheckutil.SendEmail(fmt.Sprintf("/home/golanguser/files/.%s-%s.txt", pod.Name, pod.Namespace), "recovered", fmt.Sprintf("pod %s which was previously waiting/terminated with non exit code 0 is now either running/completed in namespace %s in cluster %s ", pod.Name, pod.Namespace, runningHost), runningHost, spec)
						}
					}
				}
			}
		}
	}
	if files, err := os.ReadDir("/home/golanguser/files/"); err != nil {
		if len(files) < 1 {
			status.Healthy = true
		} else {
			status.Healthy = false
		}
	}
}

func OnPodUpdate(newObj interface{}, spec *ocphealthcheckv1.OcpHealthCheckSpec, status *ocphealthcheckv1.OcpHealthCheckStatus, runningHost string, clientset *kubernetes.Clientset) {
	newPo := new(corev1.Pod)
	err := ocphealthcheckutil.ConvertUnStructureToStructured(newObj, newPo)
	if err != nil {
		log.Log.Error(err, "failed to convert")
		return
	}
	if newPo.DeletionTimestamp != nil {
		// assuming it is deletion, so ignoring
		return
	}
	if sameNs, err := policyutil.IsChildPolicyNamespace(clientset, newPo.Namespace); err != nil {
		log.Log.Info("unable to retrieve policy object namespace")
		return
	} else if sameNs {
		ocphealthcheckutil.SendEmail(fmt.Sprintf("cgu update in progress for namespace %s", newPo.Namespace), "faulty", fmt.Sprintf("possible CGU update is in progress for objects in namespace %s in cluster %s, no pod update alerts will be sent until CGU is compliant, please execute <oc get pods -n %s and oc get policy -A> to validate", newPo.Namespace, runningHost, newPo.Namespace), runningHost, spec)
		return
	} else {
		ocphealthcheckutil.SendEmail(fmt.Sprintf("/home/golanguser/files/.%s-%s.txt", "cgu", newPo.Namespace), "recovered", fmt.Sprintf("Appears that CGU update is completed for objects in namespace %s in cluster %s, pod update alerts will continue to be sent", newPo.Namespace, runningHost), runningHost, spec)
	}
	for _, newCont := range newPo.Status.ContainerStatuses {
		if newCont.State.Terminated != nil && newCont.State.Terminated.ExitCode != 0 {
			ocphealthcheckutil.SendEmail(fmt.Sprintf("/home/golanguser/files/.%s-%s.txt", newPo.Name, newPo.Namespace), "faulty", fmt.Sprintf("pod %s's container %s is terminated with non exit code 0 in namespace %s in cluster %s", newPo.Name, newCont.Name, newPo.Namespace, runningHost), runningHost, spec)
		} else if newCont.State.Running != nil || (newCont.State.Terminated != nil && newCont.State.Terminated.ExitCode == 0) {
			// Assuming if pod has moved back to running from CrashLoopBackOff/others, the restart count will always be greater than 0
			if newCont.RestartCount > 0 {
				if newCont.LastTerminationState.Terminated != nil && newCont.LastTerminationState.Terminated.ExitCode != 0 && newCont.LastTerminationState.Terminated.FinishedAt.String() != "" {
					if PodLastRestartTimerUp(newCont.LastTerminationState.Terminated.FinishedAt.String()) {
						ocphealthcheckutil.SendEmail(fmt.Sprintf("/home/golanguser/files/.%s-%s.txt", newPo.Name, newPo.Namespace), "faulty", fmt.Sprintf("pod %s's container %s whic was previously terminate with non exit code 0 is now either running/completed in namespace %s in cluster %s", newPo.Name, newCont.Name, newPo.Namespace, runningHost), runningHost, spec)
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
									ocphealthcheckutil.SendEmail(fmt.Sprintf("/home/golanguser/files/.%s-%s.txt", newPo, newPo.Namespace), "faulty", fmt.Sprintf("Pod %s's container %s is in CrashLoopBackOff state, configured PVC %s doesn't exist in namespace %s in cluster %s, please execute <oc get pods %s -n %s -o json | jq .spec.volumes[] and oc get pvc %s -n %s -o json | jq .spec.volumeName> to validate it", newPo.Name, newCont.Name, pvc.Name, pvc.Namespace, runningHost, newPo.Name, newPo.Namespace, pvc.Name, pvc.Namespace), runningHost, spec)
								} else {
									ocphealthcheckutil.SendEmail(fmt.Sprintf("/home/golanguser/files/.%s-%s.txt", newPo, newPo.Namespace), "faulty", fmt.Sprintf("Pod %s's container %s is in CrashLoopBackOff state, unable to retrieve configured PVC %s in namespace %s in cluster %s, please execute <oc get pods %s -n %s -o json | jq .spec.volumes[] and oc get pvc %s -n %s -o json | jq .spec.volumeName> to validate it", newPo.Name, newCont.Name, pvc.Name, pvc.Namespace, runningHost, newPo.Name, newPo.Namespace, pvc.Name, pvc.Namespace), runningHost, spec)
								}
							}
							if affected, err := ocphealthcheckutil.PvHasDifferentNode(clientset, pvc.Spec.VolumeName, newPo.Spec.NodeName); err != nil {
								ocphealthcheckutil.SendEmail(fmt.Sprintf("/home/golanguser/files/.%s-%s.txt", newPo, newPo.Namespace), "faulty", fmt.Sprintf("Pod %s's container %s is in CrashLoopBackOff state, unable to retrieve volume attachment of volume %s in cluster %s, please execute <oc get pods %s -n %s -o json | jq .status.containerStatuses and oc get volumeattachments | grep %s > to validate it", newPo.Name, newCont.Name, pvc.Spec.VolumeName, runningHost, newPo, newPo.Namespace, pvc.Spec.VolumeName), runningHost, spec)
							} else if err == nil && affected {
								ocphealthcheckutil.SendEmail(fmt.Sprintf("/home/golanguser/files/.%s-%s.txt", newPo, newPo.Namespace), "faulty", fmt.Sprintf("Pod %s's container %s is in CrashLoopBackOff state, volume attachment of volume %s is mounted on a different node in cluster %s, please execute <oc get pods %s -n %s -o json | jq .status.containerStatuses and oc get volumeattachments | grep %s > to validate it", newPo.Name, newCont.Name, pvc.Spec.VolumeName, runningHost, newPo, newPo.Namespace, pvc.Spec.VolumeName), runningHost, spec)
							} else {
								// Check if it is due to other issues
								for _, cont := range newPo.Spec.Containers {
									if cont.Name == newCont.Name {
										if newCont.Image != cont.Image {
											ocphealthcheckutil.SendEmail(fmt.Sprintf("/home/golanguser/files/.%s-%s.txt", newPo, newPo.Namespace), "faulty", fmt.Sprintf("Pod %s's container %s is in CrashLoopBackOff state, volume attachment of volume %s is mounted on the SAME node, could be other issues in cluster %s, please execute <oc get pods %s -n %s -o json | jq .status.containerStatuses and oc get volumeattachments | grep %s > to validate it", newPo.Name, newCont.Name, pvc.Spec.VolumeName, runningHost, newPo, newPo.Namespace, pvc.Spec.VolumeName), runningHost, spec)
										}
									}
								}
							}
						} else {
							for _, cont := range newPo.Spec.Containers {
								if cont.Name == newCont.Name {
									if newCont.Image != cont.Image {
										ocphealthcheckutil.SendEmail(fmt.Sprintf("/home/golanguser/files/.%s-%s.txt", newPo, newPo.Namespace), "faulty", fmt.Sprintf("Pod %s's container %s is in CrashLoopBackOff state, appears to be ErrImagePull error in cluster %s, please execute <oc get pods %s -n %s -o json | jq .status.containerStatuses and oc  > to validate it", newPo.Name, newCont.Name, runningHost, newPo, newPo.Namespace), runningHost, spec)
									} else {
										ocphealthcheckutil.SendEmail(fmt.Sprintf("/home/golanguser/files/.%s-%s.txt", newPo.Name, newPo.Namespace), "faulty", fmt.Sprintf("Pod %s's container %s is in CrashLoopBackOff state, no persistent volume is attached to the pod, doesn't seem to be ErrImagePull, could be other issues, in cluster %s, please execute <oc get pods %s -n %s -o json | jq .status.containerStatuses> to validate it", newPo.Name, newCont.Name, runningHost, newPo.Name, newPo.Namespace), runningHost, spec)
									}
								}
							}
						}
					}
				}
			} else if newCont.State.Waiting.Reason == "ErrImagePull" {
				ocphealthcheckutil.SendEmail(fmt.Sprintf("/home/golanguser/files/.%s-%s.txt", newPo.Name, newPo.Namespace), "faulty", fmt.Sprintf("pod %s's container %s is failing in namespace %s due to ErrImagePull in cluster %s", newPo.Name, newCont.Name, newPo.Namespace, runningHost), runningHost, spec)
			}
		} else {
			// Assuming if pod has moved back to running from CrashLoopBackOff/others, the restart count will always be greater than 0
			if newCont.RestartCount > 0 {
				if newCont.LastTerminationState.Terminated != nil && newCont.LastTerminationState.Terminated.ExitCode != 0 && newCont.LastTerminationState.Terminated.FinishedAt.String() != "" {
					if PodLastRestartTimerUp(newCont.LastTerminationState.Terminated.FinishedAt.String()) {
						ocphealthcheckutil.SendEmail(fmt.Sprintf("/home/golanguser/files/.%s-%s.txt", newPo.Name, newPo.Namespace), "recovered", fmt.Sprintf("pod %s's container %s which was previously terminated with non exit code 0 is now either running/completed in namespace %s in cluster %s ", newPo.Name, newCont.Name, newPo.Namespace, runningHost), runningHost, spec)
					}
				} else if newCont.LastTerminationState.Terminated != nil && newCont.LastTerminationState.Terminated.ExitCode == 0 {
					// this condition will satisfy the pod that was previously running/completed and went into issues (due to image pull for example) and becomes running/completed
					ocphealthcheckutil.SendEmail(fmt.Sprintf("/home/golanguser/files/.%s-%s.txt", newPo.Name, newPo.Namespace), "recovered", fmt.Sprintf("pod %s's container %s which was previously terminated with non exit code 0 is now either running/completed in namespace %s in cluster %s ", newPo.Name, newCont.Name, newPo.Namespace, runningHost), runningHost, spec)
				}
			} else {
				// this condition will satisfy the pod that was never in running state (due to image pull for example) and becomes running/completed
				if newCont.LastTerminationState.Terminated == nil {
					ocphealthcheckutil.SendEmail(fmt.Sprintf("/home/golanguser/files/.%s-%s.txt", newPo.Name, newPo.Namespace), "recovered", fmt.Sprintf("pod %s's container %s which was previously terminated/waiting is now either running/completed in namespace %s in cluster %s ", newPo.Name, newCont.Name, newPo.Namespace, runningHost), runningHost, spec)
				}
			}
		}
	}
}
