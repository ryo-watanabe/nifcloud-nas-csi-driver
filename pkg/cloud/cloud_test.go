package cloud

import (
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"golang.org/x/net/context"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/awserr"
	"github.com/nifcloud/nifcloud-sdk-go/service/computing"
	"github.com/nifcloud/nifcloud-sdk-go/service/computing/computingiface"
	"github.com/nifcloud/nifcloud-sdk-go/service/hatoba"
	"github.com/nifcloud/nifcloud-sdk-go/service/hatoba/hatobaiface"
	"github.com/nifcloud/nifcloud-sdk-go/service/nas"
	"github.com/nifcloud/nifcloud-sdk-go/service/nas/nasiface"
	"github.com/nifcloud/nifcloud-sdk-go/service/rdb"
	"github.com/nifcloud/nifcloud-sdk-go/service/rdb/rdbiface"
)

func TestGetNasInstance(t *testing.T) {
	nasname := "testNasName"
	cases := map[string]struct {
		nasname string
		exp     interface{}
		errmsg  string
	}{
		"found": {
			nasname: "testNasName",
			exp:     nas.NASInstance{NASInstanceIdentifier: &nasname},
		},
		"not found": {
			nasname: "foo",
			errmsg:  "Client.InvalidParameter.NotFound.NASInstanceIdentifier",
		},
	}
	for name, c := range cases {
		t.Logf("====== Test case [%s] :", name)
		cloud := &Cloud{
			Nas: &mockNasClient{},
		}
		output, err := cloud.GetNasInstance(context.TODO(), c.nasname)
		if err != nil {
			checkResult(t, nil, c.exp, name, c.errmsg, err)
		} else {
			checkResult(t, *output, c.exp, name, c.errmsg, err)
		}
	}
}

func TestListNasInstance(t *testing.T) {
	nasname1 := "testNasName1"
	nasname2 := "testNasName2"
	cases := map[string]struct {
		output *nas.DescribeNASInstancesOutput
		exp    interface{}
		errmsg string
	}{
		"found": {
			output: &nas.DescribeNASInstancesOutput{
				NASInstances: []nas.NASInstance{
					nas.NASInstance{NASInstanceIdentifier: &nasname1},
					nas.NASInstance{NASInstanceIdentifier: &nasname2},
				},
			},
			exp: []nas.NASInstance{
				nas.NASInstance{NASInstanceIdentifier: &nasname1},
				nas.NASInstance{NASInstanceIdentifier: &nasname2},
			},
		},
	}
	for name, c := range cases {
		t.Logf("====== Test case [%s] :", name)
		cloud := &Cloud{
			Nas: &mockNasClient{Output: c.output},
		}
		output, err := cloud.ListNasInstances(context.TODO())
		if err != nil {
			checkResult(t, nil, c.exp, name, c.errmsg, err)
		} else {
			checkResult(t, output, c.exp, name, c.errmsg, err)
		}
	}
}

func TestCreateNasInstance(t *testing.T) {
	nasname := "testNasName"
	cases := map[string]struct {
		input  *nas.CreateNASInstanceInput
		exp    interface{}
		errmsg string
	}{
		"created": {
			input: &nas.CreateNASInstanceInput{NASInstanceIdentifier: &nasname},
			exp:   nas.NASInstance{NASInstanceIdentifier: &nasname},
		},
		"name not set": {
			input:  &nas.CreateNASInstanceInput{},
			errmsg: "Client.InvalidParameter.Reqiured.NASInstanceIdentifier",
		},
	}
	for name, c := range cases {
		t.Logf("====== Test case [%s] :", name)
		cloud := &Cloud{
			Nas: &mockNasClient{},
		}
		output, err := cloud.CreateNasInstance(context.TODO(), c.input)
		if err != nil {
			checkResult(t, nil, c.exp, name, c.errmsg, err)
		} else {
			checkResult(t, *output, c.exp, name, c.errmsg, err)
		}
	}
}

func TestModifyNasInstance(t *testing.T) {
	nasname := "testNasName"
	nrs := "true"
	cases := map[string]struct {
		nasname string
		exp     interface{}
		errmsg  string
	}{
		"modified": {
			nasname: "testNasName",
			exp: nas.NASInstance{
				NASInstanceIdentifier: &nasname,
				NoRootSquash:          &nrs,
			},
		},
		"not found": {
			nasname: "foo",
			errmsg:  "Client.InvalidParameter.NotFound.NASInstanceIdentifier",
		},
	}
	for name, c := range cases {
		t.Logf("====== Test case [%s] :", name)
		cloud := &Cloud{
			Nas: &mockNasClient{},
		}
		output, err := cloud.ModifyNasInstance(context.TODO(), c.nasname)
		if err != nil {
			checkResult(t, nil, c.exp, name, c.errmsg, err)
		} else {
			checkResult(t, *output, c.exp, name, c.errmsg, err)
		}
	}
}

func TestChangeNasInstanceSecurityGroup(t *testing.T) {
	nasname := "testNasName"
	sgname := "testSGName"
	cases := map[string]struct {
		nasname string
		sgname  string
		exp     interface{}
		errmsg  string
	}{
		"modified": {
			nasname: "testNasName",
			sgname:  "testSGName",
			exp: nas.NASInstance{
				NASInstanceIdentifier: &nasname,
				NASSecurityGroups: []nas.NASSecurityGroup{
					nas.NASSecurityGroup{NASSecurityGroupName: &sgname},
				},
			},
		},
		"not found": {
			nasname: "testNasName",
			sgname:  "foo",
			errmsg:  "Client.InvalidParameter.NotFound.NASSecurityGroupName",
		},
	}
	for name, c := range cases {
		t.Logf("====== Test case [%s] :", name)
		cloud := &Cloud{
			Nas: &mockNasClient{},
		}
		output, err := cloud.ChangeNasInstanceSecurityGroup(context.TODO(), c.nasname, c.sgname)
		if err != nil {
			checkResult(t, nil, c.exp, name, c.errmsg, err)
		} else {
			checkResult(t, *output, c.exp, name, c.errmsg, err)
		}
	}
}

func TestDeleteNasInstance(t *testing.T) {
	cases := map[string]struct {
		nasname string
		errmsg  string
	}{
		"deleted": {
			nasname: "testNasName",
		},
		"not found": {
			nasname: "foo",
			errmsg:  "Client.InvalidParameter.NotFound.NASInstanceIdentifier",
		},
	}
	for name, c := range cases {
		t.Logf("====== Test case [%s] :", name)
		cloud := &Cloud{
			Nas: &mockNasClient{},
		}
		err := cloud.DeleteNasInstance(context.TODO(), c.nasname)
		checkResult(t, nil, nil, name, c.errmsg, err)
	}
}

func TestGetNasInstanceFromVolumeId(t *testing.T) {
	nasname := "testNasName"
	n := nas.NASInstance{NASInstanceIdentifier: &nasname}
	cloud := &Cloud{
		Nas:    &mockNasClient{},
		Region: "testRegion",
	}
	volumeID := cloud.GenerateVolumeIDFromNasInstance(&n)
	if volumeID != "testRegion/testNasName" {
		t.Errorf("volumeID not matched : %s", volumeID)
	}

	cases := map[string]struct {
		nasname string
		errmsg  string
	}{
		"found": {
			nasname: "testRegion/testNasName",
		},
		"not found": {
			nasname: "testNasName",
			errmsg:  "volume id \"testNasName\" unexpected format: got 1 tokens",
		},
	}
	for name, c := range cases {
		output, err := cloud.GetNasInstanceFromVolumeID(context.TODO(), c.nasname)
		if err != nil {
			checkResult(t, nil, n, name, c.errmsg, err)
		} else {
			checkResult(t, *output, n, name, c.errmsg, err)
		}
	}
}

func TestGetNasSecurityGroup(t *testing.T) {
	sgname := "testSGName"
	cases := map[string]struct {
		sgname string
		exp    interface{}
		errmsg string
	}{
		"found": {
			sgname: "testSGName",
			exp:    nas.NASSecurityGroup{NASSecurityGroupName: &sgname},
		},
		"not found": {
			sgname: "foo",
			errmsg: "Client.InvalidParameter.NotFound.NASSecurityGroupName",
		},
	}
	for name, c := range cases {
		t.Logf("====== Test case [%s] :", name)
		cloud := &Cloud{
			Nas: &mockNasClient{},
		}
		output, err := cloud.GetNasSecurityGroup(context.TODO(), c.sgname)
		if err != nil {
			checkResult(t, nil, c.exp, name, c.errmsg, err)
		} else {
			checkResult(t, *output, c.exp, name, c.errmsg, err)
		}
	}
}

func TestCreateNasSecurityGroup(t *testing.T) {
	sgname := "testSGName"
	cases := map[string]struct {
		input  *nas.CreateNASSecurityGroupInput
		exp    interface{}
		errmsg string
	}{
		"created": {
			input: &nas.CreateNASSecurityGroupInput{NASSecurityGroupName: &sgname},
			exp:   nas.NASSecurityGroup{NASSecurityGroupName: &sgname},
		},
		"name not set": {
			input:  &nas.CreateNASSecurityGroupInput{},
			errmsg: "Client.InvalidParameter.Required.NASSecurityGroupName",
		},
	}
	for name, c := range cases {
		t.Logf("====== Test case [%s] :", name)
		cloud := &Cloud{
			Nas: &mockNasClient{},
		}
		output, err := cloud.CreateNasSecurityGroup(context.TODO(), c.input)
		if err != nil {
			checkResult(t, nil, c.exp, name, c.errmsg, err)
		} else {
			checkResult(t, *output, c.exp, name, c.errmsg, err)
		}
	}
}

var (
	statusAuthorizing = "authorizing"
	statusRevoking    = "revoking"
)

func TestAuthorizeCIDRIP(t *testing.T) {
	sgname := "testSGName"
	cidrip := "192.168.0.1/32"
	cases := map[string]struct {
		sgname string
		cidrip string
		exp    interface{}
		errmsg string
	}{
		"authorized": {
			sgname: "testSGName",
			cidrip: "192.168.0.1/32",
			exp: nas.NASSecurityGroup{
				NASSecurityGroupName: &sgname,
				IPRanges: []nas.IPRange{
					nas.IPRange{
						CIDRIP: &cidrip,
						Status: &statusAuthorizing,
					},
				},
			},
		},
		"not found": {
			sgname: "foo",
			cidrip: "192.168.0.1/32",
			errmsg: "Client.InvalidParameter.Required.NASSecurityGroupName",
		},
	}
	for name, c := range cases {
		t.Logf("====== Test case [%s] :", name)
		cloud := &Cloud{
			Nas: &mockNasClient{},
		}
		output, err := cloud.AuthorizeCIDRIP(context.TODO(), c.sgname, c.cidrip)
		if err != nil {
			checkResult(t, nil, c.exp, name, c.errmsg, err)
		} else {
			checkResult(t, *output, c.exp, name, c.errmsg, err)
		}
	}
}

func TestRevokeCIDRIP(t *testing.T) {
	sgname := "testSGName"
	cidrip := "192.168.0.1/32"
	cases := map[string]struct {
		sgname string
		cidrip string
		exp    interface{}
		errmsg string
	}{
		"revoked": {
			sgname: "testSGName",
			cidrip: "192.168.0.1/32",
			exp: nas.NASSecurityGroup{
				NASSecurityGroupName: &sgname,
				IPRanges: []nas.IPRange{
					nas.IPRange{
						CIDRIP: &cidrip,
						Status: &statusRevoking,
					},
				},
			},
		},
		"not found": {
			sgname: "testSGName",
			cidrip: "192.168.1.1/32",
			errmsg: "Client.InvalidParameter.NotFound.CIDRIP",
		},
	}
	for name, c := range cases {
		t.Logf("====== Test case [%s] :", name)
		cloud := &Cloud{
			Nas: &mockNasClient{},
		}
		output, err := cloud.RevokeCIDRIP(context.TODO(), c.sgname, c.cidrip)
		if err != nil {
			checkResult(t, nil, c.exp, name, c.errmsg, err)
		} else {
			checkResult(t, *output, c.exp, name, c.errmsg, err)
		}
	}
}

func TestListClusters(t *testing.T) {
	clustername1 := "testClusterName1"
	clustername2 := "testClusterName2"
	cases := map[string]struct {
		output *hatoba.ListClustersOutput
		exp    interface{}
		errmsg string
	}{
		"found": {
			output: &hatoba.ListClustersOutput{
				Clusters: []hatoba.Cluster{
					hatoba.Cluster{Name: &clustername1},
					hatoba.Cluster{Name: &clustername2},
				},
			},
			exp: []hatoba.Cluster{
				hatoba.Cluster{Name: &clustername1},
				hatoba.Cluster{Name: &clustername2},
			},
		},
	}
	for name, c := range cases {
		t.Logf("====== Test case [%s] :", name)
		cloud := &Cloud{
			Hatoba: &mockHatobaClient{Output: c.output},
		}
		output, err := cloud.ListClusters(context.TODO())
		if err != nil {
			checkResult(t, nil, c.exp, name, c.errmsg, err)
		} else {
			checkResult(t, output, c.exp, name, c.errmsg, err)
		}
	}
}

func TestListInstances(t *testing.T) {
	instancename1 := "testInstanceName1"
	instancename2 := "testInstanceName2"
	cases := map[string]struct {
		output *computing.DescribeInstancesOutput
		exp    interface{}
		errmsg string
	}{
		"found": {
			output: &computing.DescribeInstancesOutput{
				ReservationSet: []computing.ReservationSet{
					computing.ReservationSet{
						InstancesSet: []computing.InstancesSet{
							computing.InstancesSet{InstanceId: &instancename1},
						},
					},
					computing.ReservationSet{
						InstancesSet: []computing.InstancesSet{
							computing.InstancesSet{InstanceId: &instancename2},
						},
					},
				},
			},
			exp: []computing.InstancesSet{
				computing.InstancesSet{InstanceId: &instancename1},
				computing.InstancesSet{InstanceId: &instancename2},
			},
		},
	}
	for name, c := range cases {
		t.Logf("====== Test case [%s] :", name)
		cloud := &Cloud{
			Computing: &mockComputingClient{Output: c.output},
		}
		output, err := cloud.ListInstances(context.TODO())
		if err != nil {
			checkResult(t, nil, c.exp, name, c.errmsg, err)
		} else {
			checkResult(t, output, c.exp, name, c.errmsg, err)
		}
	}
}

func TestGetPrivateLan(t *testing.T) {
	lanname := "testLanName"
	cases := map[string]struct {
		lanname string
		exp     interface{}
		errmsg  string
	}{
		"found": {
			lanname: "testLanName",
			exp:     computing.PrivateLanSet{NetworkId: &lanname},
		},
		"not found": {
			lanname: "foo",
			errmsg:  "InvalidParameterNotFound.NetworkId",
		},
	}
	for name, c := range cases {
		t.Logf("====== Test case [%s] :", name)
		cloud := &Cloud{
			Computing: &mockComputingClient{},
		}
		output, err := cloud.GetPrivateLan(context.TODO(), c.lanname)
		if err != nil {
			checkResult(t, nil, c.exp, name, c.errmsg, err)
		} else {
			checkResult(t, *output, c.exp, name, c.errmsg, err)
		}
	}
}

func TestGetDhcpStatus(t *testing.T) {
	typeStatic := "static"
	ipAddress := "192.168.0.31"
	startIP := "192.168.10.1"
	stopIP := "192.168.10.127"
	cases := map[string]struct {
		lanname  string
		routerid string
		expPools interface{}
		expIps   interface{}
		errmsg   string
	}{
		"found": {
			lanname:  "testLanName",
			routerid: "testRouterName",
			expPools: []computing.IpAddressPoolSet{
				computing.IpAddressPoolSet{
					StartIpAddress: &startIP,
					StopIpAddress:  &stopIP,
				},
			},
			expIps: []computing.DhcpIpAddressSet{
				computing.DhcpIpAddressSet{
					LeaseType: &typeStatic,
					IpAddress: &ipAddress,
				},
			},
		},
		"not found": {
			errmsg: "InvalidParameterNotFound.RouterId",
		},
	}
	for name, c := range cases {
		t.Logf("====== Test case [%s] :", name)
		cloud := &Cloud{
			Computing: &mockComputingClient{},
		}
		pools, ips, err := cloud.GetDhcpStatus(context.TODO(), c.lanname, c.routerid)
		if err != nil {
			checkResult(t, nil, nil, name, c.errmsg, err)
		} else {
			checkResult(t, pools, c.expPools, name, c.errmsg, err)
			if !reflect.DeepEqual(ips, c.expIps) {
				t.Errorf("output not matched in case [%s]\nexpected : %v\nbut got  : %v", name, ips, c.expIps)
			}
		}
	}
}

func TestListRdbInstances(t *testing.T) {
	rdbname1 := "testRdbName1"
	rdbname2 := "testRdbName2"
	cases := map[string]struct {
		output *rdb.DescribeDBInstancesOutput
		exp    interface{}
		errmsg string
	}{
		"found": {
			output: &rdb.DescribeDBInstancesOutput{
				DBInstances: []rdb.DBInstance{
					rdb.DBInstance{DBName: &rdbname1},
					rdb.DBInstance{DBName: &rdbname2},
				},
			},
			exp: []rdb.DBInstance{
				rdb.DBInstance{DBName: &rdbname1},
				rdb.DBInstance{DBName: &rdbname2},
			},
		},
	}
	for name, c := range cases {
		t.Logf("====== Test case [%s] :", name)
		cloud := &Cloud{
			Rdb: &mockRdbClient{Output: c.output},
		}
		output, err := cloud.ListRdbInstances(context.TODO())
		if err != nil {
			checkResult(t, nil, c.exp, name, c.errmsg, err)
		} else {
			checkResult(t, output, c.exp, name, c.errmsg, err)
		}
	}
}

// Check util

func checkResult(t *testing.T, exp, got interface{}, name, errmsg string, err error) {
	if errmsg == "" {
		if err != nil {
			t.Errorf("unexpected error in case [%s] : %s", name, err.Error())
		} else if !reflect.DeepEqual(got, exp) {
			t.Errorf("output not matched in case [%s]\nexpected : %v\nbut got  : %v", name, got, exp)
		}
	} else {
		if err == nil {
			t.Errorf("expected error not occurred in case [%s]\nexpected : %s", name, errmsg)
		} else if !strings.Contains(err.Error(), errmsg) {
			t.Errorf("error message not matched in case [%s]\nmust contains : %s\nbut got : %s", name, errmsg, err.Error())
		}
	}
}

// Mock Nas service Impl.

type mockNasClient struct {
	nasiface.ClientAPI
	Output interface{}
	Err    error
}

func mockRequest(m *mockNasClient) *aws.Request {
	req := &aws.Request{
		HTTPRequest:  &http.Request{},
		HTTPResponse: &http.Response{},
		Retryer:      aws.NoOpRetryer{},
	}
	if m.Output != nil {
		req.Data = m.Output
	} else if m.Err != nil {
		req.Error = m.Err
	}
	return req
}

func (m *mockNasClient) DescribeNASInstancesRequest(
	input *nas.DescribeNASInstancesInput) nas.DescribeNASInstancesRequest {
	if input.NASInstanceIdentifier == nil {
		// Do nothing
	} else {
		if pstr(input.NASInstanceIdentifier) == "testNasName" {
			m.Output = &nas.DescribeNASInstancesOutput{
				NASInstances: []nas.NASInstance{
					nas.NASInstance{NASInstanceIdentifier: input.NASInstanceIdentifier},
				},
			}
		} else {
			m.Err = awserr.New("Client.InvalidParameter.NotFound.NASInstanceIdentifier", "", fmt.Errorf(""))
		}
	}
	return nas.DescribeNASInstancesRequest{Request: mockRequest(m)}
}

func (m *mockNasClient) CreateNASInstanceRequest(input *nas.CreateNASInstanceInput) nas.CreateNASInstanceRequest {
	if input.NASInstanceIdentifier == nil {
		m.Err = awserr.New("Client.InvalidParameter.Reqiured.NASInstanceIdentifier", "", fmt.Errorf(""))
	} else {
		m.Output = &nas.CreateNASInstanceOutput{
			NASInstance: &nas.NASInstance{NASInstanceIdentifier: input.NASInstanceIdentifier},
		}
	}
	return nas.CreateNASInstanceRequest{Request: mockRequest(m)}
}

func (m *mockNasClient) ModifyNASInstanceRequest(input *nas.ModifyNASInstanceInput) nas.ModifyNASInstanceRequest {
	if pstr(input.NASInstanceIdentifier) != "testNasName" {
		m.Err = awserr.New("Client.InvalidParameter.NotFound.NASInstanceIdentifier", "", fmt.Errorf(""))
	} else if len(input.NASSecurityGroups) > 0 && input.NASSecurityGroups[0] != "testSGName" {
		m.Err = awserr.New("Client.InvalidParameter.NotFound.NASSecurityGroupName", "", fmt.Errorf(""))
	} else {
		n := &nas.ModifyNASInstanceOutput{
			NASInstance: &nas.NASInstance{
				NASInstanceIdentifier: input.NASInstanceIdentifier,
				NoRootSquash:          input.NoRootSquash,
			},
		}
		if len(input.NASSecurityGroups) > 0 {
			n.NASInstance.NASSecurityGroups = []nas.NASSecurityGroup{
				nas.NASSecurityGroup{NASSecurityGroupName: &input.NASSecurityGroups[0]},
			}
		}
		m.Output = n
	}
	return nas.ModifyNASInstanceRequest{Request: mockRequest(m)}
}

func (m *mockNasClient) DeleteNASInstanceRequest(input *nas.DeleteNASInstanceInput) nas.DeleteNASInstanceRequest {
	if pstr(input.NASInstanceIdentifier) != "testNasName" {
		m.Err = awserr.New("Client.InvalidParameter.NotFound.NASInstanceIdentifier", "", fmt.Errorf(""))
	} else {
		m.Output = &nas.DeleteNASInstanceOutput{
			NASInstance: &nas.NASInstance{
				NASInstanceIdentifier: input.NASInstanceIdentifier,
			},
		}
	}
	return nas.DeleteNASInstanceRequest{Request: mockRequest(m)}
}

func (m *mockNasClient) DescribeNASSecurityGroupsRequest(
	input *nas.DescribeNASSecurityGroupsInput) nas.DescribeNASSecurityGroupsRequest {
	if input.NASSecurityGroupName == nil {
		// Do nothing
	} else {
		if pstr(input.NASSecurityGroupName) == "testSGName" {
			m.Output = &nas.DescribeNASSecurityGroupsOutput{
				NASSecurityGroups: []nas.NASSecurityGroup{
					nas.NASSecurityGroup{NASSecurityGroupName: input.NASSecurityGroupName},
				},
			}
		} else {
			m.Err = awserr.New("Client.InvalidParameter.NotFound.NASSecurityGroupName", "", fmt.Errorf(""))
		}
	}
	return nas.DescribeNASSecurityGroupsRequest{Request: mockRequest(m)}
}

func (m *mockNasClient) CreateNASSecurityGroupRequest(
	input *nas.CreateNASSecurityGroupInput) nas.CreateNASSecurityGroupRequest {
	if input.NASSecurityGroupName == nil {
		m.Err = awserr.New("Client.InvalidParameter.Required.NASSecurityGroupName", "", fmt.Errorf(""))
	} else {
		m.Output = &nas.CreateNASSecurityGroupOutput{
			NASSecurityGroup: &nas.NASSecurityGroup{NASSecurityGroupName: input.NASSecurityGroupName},
		}
	}
	return nas.CreateNASSecurityGroupRequest{Request: mockRequest(m)}
}

func (m *mockNasClient) AuthorizeNASSecurityGroupIngressRequest(
	input *nas.AuthorizeNASSecurityGroupIngressInput) nas.AuthorizeNASSecurityGroupIngressRequest {
	if pstr(input.NASSecurityGroupName) != "testSGName" {
		m.Err = awserr.New("Client.InvalidParameter.Required.NASSecurityGroupName", "", fmt.Errorf(""))
	} else {
		m.Output = &nas.AuthorizeNASSecurityGroupIngressOutput{
			NASSecurityGroup: &nas.NASSecurityGroup{
				NASSecurityGroupName: input.NASSecurityGroupName,
				IPRanges: []nas.IPRange{
					nas.IPRange{
						CIDRIP: input.CIDRIP,
						Status: &statusAuthorizing,
					},
				},
			},
		}
	}
	return nas.AuthorizeNASSecurityGroupIngressRequest{Request: mockRequest(m)}
}

func (m *mockNasClient) RevokeNASSecurityGroupIngressRequest(
	input *nas.RevokeNASSecurityGroupIngressInput) nas.RevokeNASSecurityGroupIngressRequest {
	if pstr(input.CIDRIP) != "192.168.0.1/32" {
		m.Err = awserr.New("Client.InvalidParameter.NotFound.CIDRIP", "", fmt.Errorf(""))
	} else {
		m.Output = &nas.RevokeNASSecurityGroupIngressOutput{
			NASSecurityGroup: &nas.NASSecurityGroup{
				NASSecurityGroupName: input.NASSecurityGroupName,
				IPRanges: []nas.IPRange{
					nas.IPRange{
						CIDRIP: input.CIDRIP,
						Status: &statusRevoking,
					},
				},
			},
		}
	}
	return nas.RevokeNASSecurityGroupIngressRequest{Request: mockRequest(m)}
}

// Mock Nas service Impl.

type mockHatobaClient struct {
	hatobaiface.ClientAPI
	Output interface{}
	Err    error
}

func mockHatobaRequest(m *mockHatobaClient) *aws.Request {
	req := &aws.Request{
		HTTPRequest:  &http.Request{},
		HTTPResponse: &http.Response{},
		Retryer:      aws.NoOpRetryer{},
	}
	if m.Output != nil {
		req.Data = m.Output
	} else if m.Err != nil {
		req.Error = m.Err
	}
	return req
}

func (m *mockHatobaClient) ListClustersRequest(input *hatoba.ListClustersInput) hatoba.ListClustersRequest {
	// Do nothing
	return hatoba.ListClustersRequest{Request: mockHatobaRequest(m)}
}

// Mock Computing service Impl.

type mockComputingClient struct {
	computingiface.ClientAPI
	Output interface{}
	Err    error
}

func mockComputingRequest(m *mockComputingClient) *aws.Request {
	req := &aws.Request{
		HTTPRequest:  &http.Request{},
		HTTPResponse: &http.Response{},
		Retryer:      aws.NoOpRetryer{},
	}
	if m.Output != nil {
		req.Data = m.Output
	} else if m.Err != nil {
		req.Error = m.Err
	}
	return req
}

func (m *mockComputingClient) DescribeInstancesRequest(
	input *computing.DescribeInstancesInput) computing.DescribeInstancesRequest {
	// Do nothing
	return computing.DescribeInstancesRequest{Request: mockComputingRequest(m)}
}

func (m *mockComputingClient) NiftyDescribePrivateLansRequest(
	input *computing.NiftyDescribePrivateLansInput) computing.NiftyDescribePrivateLansRequest {
	lanname := "testLanName"
	if len(input.NetworkId) > 0 && input.NetworkId[0] != "testLanName" {
		m.Err = awserr.New("InvalidParameterNotFound.NetworkId", "", fmt.Errorf(""))
	} else {
		m.Output = &computing.NiftyDescribePrivateLansOutput{
			PrivateLanSet: []computing.PrivateLanSet{
				computing.PrivateLanSet{NetworkId: &lanname},
			},
		}
	}
	return computing.NiftyDescribePrivateLansRequest{Request: mockComputingRequest(m)}
}

func (m *mockComputingClient) NiftyDescribeDhcpStatusRequest(
	input *computing.NiftyDescribeDhcpStatusInput) computing.NiftyDescribeDhcpStatusRequest {
	lanname := "testLanName"
	typeStatic := "static"
	ipAddress := "192.168.0.31"
	startIP := "192.168.10.1"
	stopIP := "192.168.10.127"
	if pstr(input.RouterId) == "" {
		m.Err = awserr.New("InvalidParameterNotFound.RouterId", "", fmt.Errorf(""))
	} else {
		m.Output = &computing.NiftyDescribeDhcpStatusOutput{
			DhcpStatusInformationSet: []computing.DhcpStatusInformationSet{
				computing.DhcpStatusInformationSet{
					NetworkId: &lanname,
					DhcpIpAddressInformation: &computing.DhcpIpAddressInformation{
						DhcpIpAddressSet: []computing.DhcpIpAddressSet{
							computing.DhcpIpAddressSet{
								LeaseType: &typeStatic,
								IpAddress: &ipAddress,
							},
						},
						IpAddressPoolSet: []computing.IpAddressPoolSet{
							computing.IpAddressPoolSet{
								StartIpAddress: &startIP,
								StopIpAddress:  &stopIP,
							},
						},
					},
				},
			},
		}
	}
	return computing.NiftyDescribeDhcpStatusRequest{Request: mockComputingRequest(m)}
}

// Mock Computing service Impl.

type mockRdbClient struct {
	rdbiface.ClientAPI
	Output interface{}
	Err    error
}

func mockRdbRequest(m *mockRdbClient) *aws.Request {
	req := &aws.Request{
		HTTPRequest:  &http.Request{},
		HTTPResponse: &http.Response{},
		Retryer:      aws.NoOpRetryer{},
	}
	if m.Output != nil {
		req.Data = m.Output
	} else if m.Err != nil {
		req.Error = m.Err
	}
	return req
}

func (m *mockRdbClient) DescribeDBInstancesRequest(
	input *rdb.DescribeDBInstancesInput) rdb.DescribeDBInstancesRequest {
	// Do nothing
	return rdb.DescribeDBInstancesRequest{Request: mockRdbRequest(m)}
}
