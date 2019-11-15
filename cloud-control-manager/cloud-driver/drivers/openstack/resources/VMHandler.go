// Proof of Concepts of CB-Spider.
// The CB-Spider is a sub-Framework of the Cloud-Barista Multi-Cloud Project.
// The CB-Spider Mission is to connect all the clouds with a single interface.
//
//      * Cloud-Barista: https://github.com/cloud-barista
//
// This is a Cloud Driver Example for PoC Test.
//
// by hyokyung.kim@innogrid.co.kr, 2019.07.

package resources

import (
	"errors"
	"fmt"
	cblog "github.com/cloud-barista/cb-log"
	irs "github.com/cloud-barista/cb-spider/cloud-control-manager/cloud-driver/interfaces/resources"
	"github.com/rackspace/gophercloud"
	"github.com/rackspace/gophercloud/openstack/compute/v2/extensions/floatingip"
	"github.com/rackspace/gophercloud/openstack/compute/v2/extensions/keypairs"
	"github.com/rackspace/gophercloud/openstack/compute/v2/extensions/startstop"
	"github.com/rackspace/gophercloud/openstack/compute/v2/servers"
	"github.com/rackspace/gophercloud/pagination"
	"github.com/sirupsen/logrus"
	"strings"
	"time"
)

var cblogger *logrus.Logger

func init() {
	// cblog is a global variable.
	cblogger = cblog.GetLogger("CB-SPIDER")
}

// modified by powerkim, 2019.07.29
type OpenStackVMHandler struct {
	Client        *gophercloud.ServiceClient
	NetworkClient *gophercloud.ServiceClient
}

// modified by powerkim, 2019.07.29
func (vmHandler *OpenStackVMHandler) StartVM(vmReqInfo irs.VMReqInfo) (irs.VMInfo, error) {

	vNetId, err := GetCBVNetId(vmHandler.NetworkClient)
	if err != nil {
		return irs.VMInfo{}, err
	}

	serverCreateOpts := servers.CreateOpts{
		Name:      vmReqInfo.VMName,
		ImageRef:  vmReqInfo.ImageId,
		FlavorRef: vmReqInfo.VMSpecId,
		Networks: []servers.Network{
			{UUID: vNetId},
		},
		SecurityGroups: vmReqInfo.SecurityGroupIds,
	}

	// Add KeyPair
	createOpts := keypairs.CreateOptsExt{
		CreateOptsBuilder: serverCreateOpts,
		KeyName:           vmReqInfo.KeyPairName,
	}

	server, err := servers.Create(vmHandler.Client, createOpts).Extract()
	if err != nil {
		return irs.VMInfo{}, err
	}

	// VM 생성 완료까지 wait
	vmId := server.ID
	var isDeployed bool
	var serverInfo irs.VMInfo

	for {
		if isDeployed {
			break
		}

		// Check VM Deploy Status
		serverResult, err := servers.Get(vmHandler.Client, vmId).Extract()
		if err != nil {
			return irs.VMInfo{}, err
		}
		if strings.ToLower(serverResult.Status) == "active" {
			// Associate Public IP
			if ok, err := vmHandler.AssociatePublicIP(serverResult.ID, vmReqInfo.PublicIPId); !ok {
				return irs.VMInfo{}, err
			}

			serverInfo = mappingServerInfo(*serverResult)
			isDeployed = true
		}

		time.Sleep(5 * time.Second)
	}

	return serverInfo, nil
}

func (vmHandler *OpenStackVMHandler) SuspendVM(vmNameID string) (irs.VMStatus, error) {
	vmID, err := vmHandler.getVmIdByName(vmNameID)
	if err != nil {
		return "", nil
	}

	err = startstop.Stop(vmHandler.Client, vmID).Err
	if err != nil {
		cblogger.Error(err)
		return irs.Failed, err
	}

	// 자체생성상태 반환 (OpenStack은 진행 중인 상태에 대한 정보 미제공)
	return irs.Suspending, nil
}

func (vmHandler *OpenStackVMHandler) ResumeVM(vmNameID string) (irs.VMStatus, error) {
	vmID, err := vmHandler.getVmIdByName(vmNameID)
	if err != nil {
		return "", nil
	}

	err = startstop.Start(vmHandler.Client, vmID).Err
	if err != nil {
		cblogger.Error(err)
		return irs.Failed, err
	}

	// 자체생성상태 반환 (OpenStack은 진행 중인 상태에 대한 정보 미제공)
	return irs.Resuming, nil
}

func (vmHandler *OpenStackVMHandler) RebootVM(vmNameID string) (irs.VMStatus, error) {
	/*rebootOpts := servers.RebootOpts{
		Type: servers.SoftReboot,
		//Type: servers.HardReboot,
	}*/
	vmID, err := vmHandler.getVmIdByName(vmNameID)
	if err != nil {
		return "", nil
	}

	rebootOpts := servers.SoftReboot
	err = servers.Reboot(vmHandler.Client, vmID, rebootOpts).ExtractErr()
	if err != nil {
		cblogger.Error(err)
		return irs.Failed, err
	}

	// 자체생성상태 반환 (OpenStack은 진행 중인 상태에 대한 정보 미제공)
	return irs.Rebooting, nil
}

func (vmHandler *OpenStackVMHandler) TerminateVM(vmNameID string) (irs.VMStatus, error) {
	vmID, err := vmHandler.getVmIdByName(vmNameID)
	if err != nil {
		return "", nil
	}

	err = servers.Delete(vmHandler.Client, vmID).ExtractErr()
	if err != nil {
		cblogger.Error(err)
		return irs.Failed, err
	}

	// 자체생성상태 반환 (OpenStack은 진행 중인 상태에 대한 정보 미제공)
	return irs.Terminating, nil
}

func (vmHandler *OpenStackVMHandler) ListVMStatus() ([]*irs.VMStatusInfo, error) {
	var vmStatusList []*irs.VMStatusInfo

	pager := servers.List(vmHandler.Client, nil)
	err := pager.EachPage(func(page pagination.Page) (bool, error) {
		// Get VM Status
		list, err := servers.ExtractServers(page)
		if err != nil {
			return false, err
		}
		// Add to List
		for _, s := range list {
			vmStatus := irs.VMStatus(s.Status)
			vmStatusInfo := irs.VMStatusInfo{
				VmId:     s.ID,
				VmStatus: vmStatus,
			}
			vmStatusList = append(vmStatusList, &vmStatusInfo)
		}
		return true, nil
	})
	if err != nil {
		cblogger.Error(err)
		return nil, err
	}

	return vmStatusList, nil
}

func (vmHandler *OpenStackVMHandler) GetVMStatus(vmNameID string) (irs.VMStatus, error) {
	vmID, err := vmHandler.getVmIdByName(vmNameID)
	if err != nil {
		return "", nil
	}

	serverResult, err := servers.Get(vmHandler.Client, vmID).Extract()
	if err != nil {
		cblogger.Error(err)
		return irs.VMStatus(""), err
	}

	// Set VM Status Info
	var resultStatus string
	switch strings.ToLower(serverResult.Status) {
	case "build":
		resultStatus = "Creating"
	case "active":
		resultStatus = "Running"
	case "shutoff":
		resultStatus = "Suspended"
	case "reboot":
		resultStatus = "Rebooting"
	case "error":
	default:
		resultStatus = "Failed"
	}
	return irs.VMStatus(resultStatus), nil
}

func (vmHandler *OpenStackVMHandler) ListVM() ([]*irs.VMInfo, error) {

	// 가상서버 목록 조회
	pager, err := servers.List(vmHandler.Client, nil).AllPages()
	if err != nil {
		return nil, err
	}
	servers, err := servers.ExtractServers(pager)
	if err != nil {
		return nil, err
	}

	// 가상서버 목록 정보 매핑
	vmList := make([]*irs.VMInfo, len(servers))
	for i, v := range servers {
		serverInfo := mappingServerInfo(v)
		vmList[i] = &serverInfo
	}
	return vmList, nil
}

func (vmHandler *OpenStackVMHandler) GetVM(vmNameID string) (irs.VMInfo, error) {
	// 기존의 vmID 기준 가상서버 조회 (old)
	/*serverResult, err := servers.Get(vmHandler.Client, vmID).Extract()
	if err != nil {
		cblogger.Info(err)
		return irs.VMInfo{}, err
	}*/

	vmID, err := vmHandler.getVmIdByName(vmNameID)
	if err != nil {
		return irs.VMInfo{}, err
	}

	serverResult, err := servers.Get(vmHandler.Client, vmID).Extract()
	if err != nil {
		cblogger.Info(err)
		return irs.VMInfo{}, err
	}

	vmInfo := mappingServerInfo(*serverResult)
	return vmInfo, nil
}

func (vmHandler *OpenStackVMHandler) AssociatePublicIP(serverID string, publicIPID string) (bool, error) {
	associateOpts := floatingip.AssociateOpts{
		ServerID:   serverID,
		FloatingIP: publicIPID,
	}
	err := floatingip.AssociateInstance(vmHandler.Client, associateOpts).ExtractErr()
	if err != nil {
		return false, err
	}
	return true, nil
}

func mappingServerInfo(server servers.Server) irs.VMInfo {

	// Get Default VM Info
	vmInfo := irs.VMInfo{
		Name:        server.Name,
		Id:          server.ID,
		KeyPairName: server.KeyName,

		VMUserId:           server.UserID,
		VMUserPasswd:       server.AdminPass,
		NetworkInterfaceId: server.HostID,

		KeyValueList:     nil,
		SecurityGroupIds: nil,
	}

	if creatTime, err := time.Parse(time.RFC3339, server.Created); err == nil {
		vmInfo.StartTime = creatTime
	}

	if len(server.Image) != 0 {
		vmInfo.ImageId = server.Image["id"].(string)
	}
	if len(server.Flavor) != 0 {
		vmInfo.VMSpecId = server.Flavor["id"].(string)
	}
	if len(server.SecurityGroups) != 0 {
		var securityGroupIdArr []string
		for _, secGroupMap := range server.SecurityGroups {
			securityGroupIdArr = append(securityGroupIdArr, fmt.Sprintf("%v", secGroupMap["name"]))
		}
		vmInfo.SecurityGroupIds = securityGroupIdArr
	}

	// Get VM Subnet, Address Info
	for k, subnet := range server.Addresses {
		vmInfo.VirtualNetworkId = k
		for _, addr := range subnet.([]interface{}) {
			addrMap := addr.(map[string]interface{})
			if addrMap["OS-EXT-IPS:type"] == "floating" {
				vmInfo.PublicIP = addrMap["addr"].(string)
			} else if addrMap["OS-EXT-IPS:type"] == "fixed" {
				vmInfo.PrivateIP = addrMap["addr"].(string)
			}
		}
	}

	return vmInfo
}

func (vmHandler *OpenStackVMHandler) getVmIdByName(vmNameID string) (string, error) {
	var vmId string

	// vmNameId 기준 가상서버 조회
	listOpts := servers.ListOpts{
		Name: vmNameID,
	}

	pager, err := servers.List(vmHandler.Client, listOpts).AllPages()
	if err != nil {
		return "", err
	}
	server, err := servers.ExtractServers(pager)
	if err != nil {
		return "", err
	}

	// 1개 이상의 가상서버가 중복 조회될 경우 에러 처리
	if len(server) > 1 {
		err := errors.New(fmt.Sprintf("failed to search vm, duplicate nameId exists, %s", vmNameID))
		return "", err
	} else {
		vmId = server[0].ID
	}

	return vmId, nil
}
