package hubutil

import (
	"context"
	"fmt"

	ocpscanv1 "github.com/barani129/ocphealthcheckinf/api/v1"
	ocphealthcheckutil "github.com/barani129/ocphealthcheckinf/internal/util"
	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

func OnMCPUpdate(staticClientSet *kubernetes.Clientset, spec *ocpscanv1.OcpHealthCheckSpec, runningHost string) {
	mcpList := mcfgv1.MachineConfigPoolList{}
	err := staticClientSet.RESTClient().Get().AbsPath("/apis/machineconfiguration.openshift.io/v1/machineconfigpools").Do(context.Background()).Into(&mcpList)
	if err != nil {
		log.Log.Error(err, "unable to retrieve mcp list")
		return
	}
	for _, mcp := range mcpList.Items {
		if spec.HubCluster != nil && *spec.HubCluster {
			if mcp.Spec.Paused {
				if mcpInProgress, node, err := ocphealthcheckutil.CheckNodeMcpAnnotations(staticClientSet, mcp.Spec.NodeSelector.MatchLabels); err != nil {
					log.Log.Error(err, "unable to retrieve MCP annotations")
					// exiting
					return
				} else if err == nil && mcpInProgress {
					ocphealthcheckutil.SendEmail("MachineConfigPool", fmt.Sprintf("/home/golanguser/files/ocphealth/.%s-%s.txt", "mcp-pause", mcp.Name), "faulty", fmt.Sprintf("MachineConfig pool %s is paused and actual update is in progress and node %s's annotation has been set to other than done in cluster %s, please execute <oc get mcp %s > to validate it", mcp.Name, node, runningHost, mcp.Name), runningHost, spec)
					return
				} else {
					ocphealthcheckutil.SendEmail("MachineConfigPool", fmt.Sprintf("/home/golanguser/files/ocphealth/.%s-%s.txt", "mcp-pause", mcp.Name), "faulty", fmt.Sprintf("MachineConfig pool %s is paused but no actual MCP update in progress in cluster %s, please execute <oc get mcp %s > to validate it", mcp.Name, runningHost, mcp.Name), runningHost, spec)
				}
			} else {
				ocphealthcheckutil.SendEmail("MachineConfigPool", fmt.Sprintf("/home/golanguser/files/ocphealth/.%s-%s.txt", "mcp-pause", mcp.Name), "recovered", fmt.Sprintf("MachineConfig pool %s is unpaused in cluster %s, please execute <oc get mcp %s > to validate it", mcp.Name, runningHost, mcp.Name), runningHost, spec)
			}
		}
		for _, cond := range mcp.Status.Conditions {
			if cond.Type == ocphealthcheckutil.MCPUpdating {
				if cond.Status == ocphealthcheckutil.NODEREADYTrue {
					// Check node annotations to validate it
					if mcp.Spec.MachineConfigSelector.MatchLabels != nil {
						if isMcPInProgress, node, err := ocphealthcheckutil.CheckNodeMcpAnnotations(staticClientSet, mcp.Spec.NodeSelector.MatchLabels); err != nil {
							return
						} else if err == nil && isMcPInProgress {
							ocphealthcheckutil.SendEmail("MachineConfigPool", fmt.Sprintf("/home/golanguser/files/ocphealth/.%s-%s.txt", "mcp", mcp.Name), "faulty", fmt.Sprintf("MachineConfig pool %s update is in progress and node %s's annotation has been set to other than done in cluster %s, please execute <oc get mcp %s > to validate it", mcp.Name, node, runningHost, mcp.Name), runningHost, spec)
							return
						} else {
							if isNodeAffected, anode, err := ocphealthcheckutil.CheckNodeReadiness(staticClientSet, mcp.Spec.MachineConfigSelector.MatchLabels); err != nil {
								// unable to verify node status
								return
							} else if err == nil && isNodeAffected {
								ocphealthcheckutil.SendEmail("MachineConfigPool", fmt.Sprintf("/home/golanguser/files/ocphealth/.%s-%s.txt", "mcp", mcp.Name), "faulty", fmt.Sprintf("MachineConfig pool %s update has been set to true, due to possible manual action on node %s in cluster %s, please execute <oc get mcp %s > to validate it", mcp.Name, anode, runningHost, mcp.Name), runningHost, spec)
							} else {
								ocphealthcheckutil.SendEmail("MachineConfigPool", fmt.Sprintf("/home/golanguser/files/ocphealth/.%s-%s.txt", "mcp", mcp.Name), "faulty", fmt.Sprintf("MachineConfig pool %s update has been set to true, nodes are healthy, mcp update is probably just starting in cluster %s, please execute <oc get mcp %s > to validate it", mcp.Name, runningHost, mcp.Name), runningHost, spec)
							}
						}
					}
				} else if cond.Status == ocphealthcheckutil.NODEREADYFalse {
					ocphealthcheckutil.SendEmail("MachineConfigPool", fmt.Sprintf("/home/golanguser/files/ocphealth/.%s-%s.txt", "mcp", mcp.Name), "recovered", fmt.Sprintf("MachineConfig pool %s update which was previously set to true is now changed to false in cluster %s, please execute <oc get mcp %s > to validate it", mcp.Name, runningHost, mcp.Name), runningHost, spec)
				}
			} else if cond.Type == ocphealthcheckutil.MACHINECONFIGUPDATEDEGRADED {
				if cond.Status == ocphealthcheckutil.NODEREADYTrue {
					ocphealthcheckutil.SendEmail("MachineConfigPool", fmt.Sprintf("/home/golanguser/files/ocphealth/.%s-%s.txt", "mcp", mcp.Name), "faulty", fmt.Sprintf("MachineConfig pool %s is degraded in cluster %s, please execute <oc get mcp %s and oc get nodes> to validate it", mcp.Name, runningHost, mcp.Name), runningHost, spec)
				} else if cond.Status == "False" {
					ocphealthcheckutil.SendEmail("MachineConfigPool", fmt.Sprintf("/home/golanguser/files/ocphealth/.%s-%s.txt", "mcp", mcp.Name), "recovered", fmt.Sprintf("MachineConfig pool %s is no longer degraded in cluster %s", mcp.Name, runningHost), runningHost, spec)
				}
			}
		}
	}
}
