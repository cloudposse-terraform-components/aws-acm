package test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go/service/acm"
	"github.com/cloudposse/test-helpers/pkg/atmos"
	helper "github.com/cloudposse/test-helpers/pkg/atmos/aws-component-helper"
	"github.com/gruntwork-io/terratest/modules/aws"
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

func TestComponent(t *testing.T) {
	// Define the AWS region to use for the tests
	awsRegion := "us-east-2"

	// Initialize the test fixture
	fixture := helper.NewFixture(t, "../", awsRegion, "test/fixtures")

	// Ensure teardown is executed after the test
	defer fixture.TearDown()
	fixture.SetUp(&atmos.Options{})

	// Define the test suite
	fixture.Suite("default", func(t *testing.T, suite *helper.Suite) {
		// Setup phase: Create DNS zones for testing
		suite.Setup(t, func(t *testing.T, atm *helper.Atmos) {
			randomID := suite.GetRandomIdentifier()
			domainName := fmt.Sprintf("example-%s.net", randomID)

			// Deploy the primary DNS zone
			inputs := map[string]interface{}{
				"domain_names": []string{domainName},
			}
			atm.GetAndDeploy("dns-primary", "default-test", inputs)

			// Deploy the delegated DNS zone
			inputs = map[string]interface{}{
				"zone_config": []map[string]interface{}{
					{
						"subdomain": randomID,
						"zone_name": domainName,
					},
				},
			}
			atm.GetAndDeploy("dns-delegated", "default-test", inputs)
		})

		// Teardown phase: Destroy the DNS zones created during setup
		suite.TearDown(t, func(t *testing.T, atm *helper.Atmos) {
			dnsPrimaryComponent := helper.NewAtmosComponent("dns-primary", "default-test", map[string]interface{}{})

			primaryZones := map[string]zone{}
			atm.OutputStruct(dnsPrimaryComponent, "zones", &primaryZones)

			primaryDomains := make([]string, 0, len(primaryZones))
			for k := range primaryZones {
				primaryDomains = append(primaryDomains, k)
			}

			primaryDomainName := primaryDomains[0]

			randomID := suite.GetRandomIdentifier()

			inputs := map[string]interface{}{
				"zone_config": []map[string]interface{}{
					{
						"subdomain": randomID,
						"zone_name": primaryDomainName,
					},
				},
			}

			atm.GetAndDestroy("dns-delegated", "default-test", inputs)
			atm.GetAndDestroy("dns-primary", "default-test", map[string]interface{}{})
		})

		// Test phase: Validate the functionality of the ACM component
		suite.Test(t, "basic", func(t *testing.T, atm *helper.Atmos) {

			// Reference the delegated DNS component
			dnsDelegatedComponent := helper.NewAtmosComponent("dns-delegated", "default-test", map[string]interface{}{})

			// Retrieve outputs from the delegated DNS component
			domainName := atm.Output(dnsDelegatedComponent, "default_domain_name")
			domainZoneId := atm.Output(dnsDelegatedComponent, "default_dns_zone_id")

			// Inputs for the ACM component
			inputs := map[string]interface{}{
				"enabled":                           true,
				"domain_name":                       domainName,
				"process_domain_validation_options": true,
				"validation_method":                 "DNS",
			}

			// Deploy the ACM component
			component := helper.NewAtmosComponent("acm/basic", "default-test", inputs)
			defer atm.Destroy(component)
			atm.Deploy(component)

			// Validate the ACM outputs
			id := atm.Output(component, "id")
			assert.NotEmpty(t, id)

			arn := atm.Output(component, "arn")
			assert.NotEmpty(t, arn)

			domainNameOuput := atm.Output(component, "domain_name")
			assert.Equal(t, domainName, domainNameOuput)

			// Verify that the ACM certificate ARN is stored in SSM
			ssmPath := fmt.Sprintf("/acm/%s", domainName)
			acmArnSssmStored := aws.GetParameter(t, awsRegion, ssmPath)
			assert.Equal(t, arn, acmArnSssmStored)

			// Validate domain validation options
			validationOptions := [][]validationOption{}
			atm.OutputStruct(component, "domain_validation_options", &validationOptions)
			for _, validationOption := range validationOptions[0] {
				if validationOption.DomainName != domainName {
					continue
				}
				assert.Equal(t, domainName, validationOption.DomainName)

				// Verify DNS validation records
				resourceRecordName := strings.TrimSuffix(validationOption.ResourceRecordName, ".")
				validationDNSRecord := aws.GetRoute53Record(t, domainZoneId, resourceRecordName, validationOption.ResourceRecordType, awsRegion)
				assert.Equal(t, validationOption.ResourceRecordValue, *validationDNSRecord.ResourceRecords[0].Value)
			}

			// Validate the ACM certificate in AWS
			client := aws.NewAcmClient(t, awsRegion)
			awsCertificate, err := client.DescribeCertificate(&acm.DescribeCertificateInput{
				CertificateArn: &arn,
			})
			require.NoError(t, err)

			// Ensure the certificate type and ARN match expectations
			assert.Equal(t, "AMAZON_ISSUED", *awsCertificate.Certificate.Type)
			assert.Equal(t, arn, *awsCertificate.Certificate.CertificateArn)
		})
	})
}
