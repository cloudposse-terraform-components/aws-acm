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

func TestComponent(t *testing.T) {
	awsRegion := "us-east-2"

	fixture := helper.NewFixture(t, "../", awsRegion, "test/fixtures")

	defer fixture.TearDown()
	fixture.SetUp(&atmos.Options{})

	fixture.Suite("default", func(t *testing.T, suite *helper.Suite) {
		// Setup the primary and delegated DNS zones
		suite.Setup(t, func(t *testing.T, atm *helper.Atmos) {
			randomID := suite.GetRandomIdentifier()
			domainName := fmt.Sprintf("example-%s.net", randomID)
			inputs := map[string]interface{}{
				"domain_names": []string{domainName},
			}
			atm.GetAndDeploy("dns-primary", "default-test", inputs)

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

		suite.TearDown(t, func(t *testing.T, atm *helper.Atmos) {
			atm.GetAndDestroy("dns-delegated", "default-test", map[string]interface{}{})
			atm.GetAndDestroy("dns-primary", "default-test", map[string]interface{}{})
		})

		suite.Test(t, "basic", func(t *testing.T, atm *helper.Atmos) {

			dnsDelegatedComponent := helper.NewAtmosComponent("dns-delegated", "default-test", map[string]interface{}{})

			domainName := atm.Output(dnsDelegatedComponent, "default_domain_name")
			domainZoneId := atm.Output(dnsDelegatedComponent, "default_dns_zone_id")

			inputs := map[string]interface{}{
				"enabled":                           true,
				"domain_name":                       domainName,
				"process_domain_validation_options": true,
				"validation_method":                 "DNS",
			}

			component := helper.NewAtmosComponent("acm/basic", "default-test", inputs)

			defer atm.Destroy(component)
			atm.Deploy(component)

			id := atm.Output(component, "id")
			assert.NotEmpty(t, id)

			arn := atm.Output(component, "arn")
			assert.NotEmpty(t, arn)

			domainNameOuput := atm.Output(component, "domain_name")
			assert.Equal(t, domainName, domainNameOuput)

			ssmPath := fmt.Sprintf("/acm/%s", domainName)
			acmArnSssmStored := aws.GetParameter(t, awsRegion, ssmPath)
			assert.Equal(t, arn, acmArnSssmStored)

			validationOptions := [][]validationOption{}
			atm.OutputStruct(component, "domain_validation_options", &validationOptions)
			for _, validationOption := range validationOptions[0] {
				if validationOption.DomainName != domainName {
					continue
				}
				assert.Equal(t, domainName, validationOption.DomainName)

				resourceRecordName := strings.TrimSuffix(validationOption.ResourceRecordName, ".")
				validationDNSRecord := aws.GetRoute53Record(t, domainZoneId, resourceRecordName, validationOption.ResourceRecordType, awsRegion)
				assert.Equal(t, validationOption.ResourceRecordValue, *validationDNSRecord.ResourceRecords[0].Value)
			}

			client := aws.NewAcmClient(t, awsRegion)
			awsCertificate, err := client.DescribeCertificate(&acm.DescribeCertificateInput{
				CertificateArn: &arn,
			})
			require.NoError(t, err)

			// We can not test issue status because DNS validation not working with mock primary domain
			// assert.Equal(t, "ISSUED", *awsCertificate.Certificate.Status)
			assert.Equal(t, "AMAZON_ISSUED", *awsCertificate.Certificate.Type)
			assert.Equal(t, arn, *awsCertificate.Certificate.CertificateArn)

		})
	})
}
