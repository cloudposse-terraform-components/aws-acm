package test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/acm"
	"github.com/aws/aws-sdk-go-v2/service/acm/types"
	"github.com/cloudposse/test-helpers/pkg/atmos"
	helper "github.com/cloudposse/test-helpers/pkg/atmos/component-helper"
	"github.com/gruntwork-io/terratest/modules/aws"
	"github.com/gruntwork-io/terratest/modules/random"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type validationOption struct {
	DomainName          string `json:"domain_name"`
	ResourceRecordName  string `json:"resource_record_name"`
	ResourceRecordType  string `json:"resource_record_type"`
	ResourceRecordValue string `json:"resource_record_value"`
}

type zone struct {
	Arn               string            `json:"arn"`
	Comment           string            `json:"comment"`
	DelegationSetId   string            `json:"delegation_set_id"`
	ForceDestroy      bool              `json:"force_destroy"`
	Id                string            `json:"id"`
	Name              string            `json:"name"`
	NameServers       []string          `json:"name_servers"`
	PrimaryNameServer string            `json:"primary_name_server"`
	Tags              map[string]string `json:"tags"`
	TagsAll           map[string]string `json:"tags_all"`
	Vpc               []struct {
		ID     string `json:"vpc_id"`
		Region string `json:"vpc_region"`
	} `json:"vpc"`
	ZoneID string `json:"zone_id"`
}

type ComponentSuite struct {
	helper.TestSuite
}

func (s *ComponentSuite) TestBasic() {
	const component = "acm/basic"
	const stack = "default-test"
	const awsRegion = "us-east-2"

	// Reference the delegated DNS component
	dnsDelegatedOptions := s.GetAtmosOptions("dns-delegated", "default-test", nil)

	// Retrieve outputs from the delegated DNS component
	delegatedDomainName := atmos.Output(s.T(), dnsDelegatedOptions, "default_domain_name")
	domainZoneId := atmos.Output(s.T(), dnsDelegatedOptions, "default_dns_zone_id")

	domainName := fmt.Sprintf("%s.%s", s.Config.RandomIdentifier, delegatedDomainName)

	// Inputs for the ACM component
	inputs := map[string]interface{}{
		"enabled":                           true,
		"process_domain_validation_options": true,
		"validation_method":                 "DNS",
		"domain_name":                       domainName,
	}

	defer s.DestroyAtmosComponent(s.T(), component, stack, &inputs)
	options, _ := s.DeployAtmosComponent(s.T(), component, stack, &inputs)

	// Validate the ACM outputs
	id := atmos.Output(s.T(), options, "id")
	assert.NotEmpty(s.T(), id)

	arn := atmos.Output(s.T(), options, "arn")
	assert.NotEmpty(s.T(), arn)

	domainNameOuput := atmos.Output(s.T(), options, "domain_name")
	assert.Equal(s.T(), domainName, domainNameOuput)

	// Verify that the ACM certificate ARN is stored in SSM
	ssmPath := fmt.Sprintf("/acm/%s", domainName)
	acmArnSssmStored := aws.GetParameter(s.T(), awsRegion, ssmPath)
	assert.Equal(s.T(), arn, acmArnSssmStored)

	// Validate domain validation options
	validationOptions := [][]validationOption{}
	atmos.OutputStruct(s.T(), options, "domain_validation_options", &validationOptions)
	for _, validationOption := range validationOptions[0] {
		if validationOption.DomainName != domainName {
			continue
		}
		assert.Equal(s.T(), domainName, validationOption.DomainName)

		// Verify DNS validation records
		resourceRecordName := strings.TrimSuffix(validationOption.ResourceRecordName, ".")
		validationDNSRecord := aws.GetRoute53Record(s.T(), domainZoneId, resourceRecordName, validationOption.ResourceRecordType, awsRegion)
		assert.Equal(s.T(), validationOption.ResourceRecordValue, *validationDNSRecord.ResourceRecords[0].Value)
	}

	// Validate the ACM certificate in AWS
	client := aws.NewAcmClient(s.T(), awsRegion)
	awsCertificate, err := client.DescribeCertificate(context.Background(), &acm.DescribeCertificateInput{
		CertificateArn: &arn,
	})
	require.NoError(s.T(), err)

	// Ensure the certificate type and ARN match expectations
	assert.Equal(s.T(), string(types.CertificateStatusIssued), string(awsCertificate.Certificate.Status))
	assert.Equal(s.T(), string(types.CertificateTypeAmazonIssued), string(awsCertificate.Certificate.Type))
	assert.Equal(s.T(), arn, *awsCertificate.Certificate.CertificateArn)

	s.DriftTest(component, stack, &inputs)
}

func (s *ComponentSuite) TestEnabledFlag() {
	const component = "acm/disabled"
	const stack = "default-test"
	s.VerifyEnabledFlag(component, stack, nil)
}

func TestRunSuite(t *testing.T) {
	suite := new(ComponentSuite)

	subdomain := strings.ToLower(random.UniqueId())
	inputs := map[string]interface{}{
		"zone_config": []map[string]interface{}{
			{
				"subdomain": subdomain,
				"zone_name": "components.cptest.test-automation.app",
			},
		},
	}
	suite.AddDependency(t, "dns-delegated", "default-test", &inputs)
	helper.Run(t, suite)
}
