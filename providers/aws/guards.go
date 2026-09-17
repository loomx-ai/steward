package aws

import (
	"context"
	"fmt"
	"strings"
	"time"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	awsbackup "github.com/aws/aws-sdk-go-v2/service/backup"
	awskms "github.com/aws/aws-sdk-go-v2/service/kms"
	kmstypes "github.com/aws/aws-sdk-go-v2/service/kms/types"
	"github.com/loomx-ai/steward/internal/core/execution"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

type KMSNativeAPI interface {
	DescribeKey(context.Context, *awskms.DescribeKeyInput, ...func(*awskms.Options)) (*awskms.DescribeKeyOutput, error)
}

type BackupNativeAPI interface {
	DescribeBackupVault(context.Context, *awsbackup.DescribeBackupVaultInput, ...func(*awsbackup.Options)) (*awsbackup.DescribeBackupVaultOutput, error)
}

// deleteGuard is a live service check that Cloud Control delete handlers do
// not express: resources the service itself owns, deletions already scheduled,
// or contents that make deletion fail.
type deleteGuard func(context.Context, string) (guardOutcome, error)

type guardOutcome struct {
	blocked  string
	pending  bool
	evidence map[string]any
}

type guardedAction struct {
	*CloudControlAction
	guard deleteGuard
}

func (a *guardedAction) check(ctx context.Context, request contracts.ActionRequest) (guardOutcome, error) {
	if err := a.validate(request); err != nil {
		return guardOutcome{}, err
	}
	return a.guard(ctx, cloudControlIdentifier(request.Asset))
}

func (a *guardedAction) Preflight(ctx context.Context, request contracts.ActionRequest) (contracts.PreflightResult, error) {
	outcome, err := a.check(ctx, request)
	if err != nil {
		return contracts.PreflightResult{}, err
	}
	if outcome.pending {
		return contracts.PreflightResult{Absent: true, Reason: "resource deletion is already scheduled", Evidence: outcome.evidence}, nil
	}
	if outcome.blocked != "" {
		return contracts.PreflightResult{Allowed: false, Reason: outcome.blocked, Evidence: outcome.evidence}, nil
	}
	result, err := a.CloudControlAction.Preflight(ctx, request)
	for key, value := range outcome.evidence {
		if result.Evidence != nil {
			result.Evidence[key] = value
		}
	}
	return result, err
}

func (a *guardedAction) Execute(ctx context.Context, request contracts.ActionRequest) (contracts.ActionResult, error) {
	outcome, err := a.check(ctx, request)
	if err != nil {
		return contracts.ActionResult{}, err
	}
	if outcome.pending {
		return contracts.ActionResult{Data: map[string]any{"phase": cloudControlPhaseDelete, "status": "SUCCESS", "guard": "deletion_scheduled"}}, nil
	}
	if outcome.blocked != "" {
		return contracts.ActionResult{}, &contracts.ProviderCallError{Provider: execution.ProviderError{
			Category: execution.ErrorConflict, Code: "DeletePreconditionFailed", Message: outcome.blocked, Summary: outcome.evidence,
		}}
	}
	return a.CloudControlAction.Execute(ctx, request)
}

func (a *guardedAction) Readback(ctx context.Context, request contracts.ActionRequest) (contracts.ReadbackResult, error) {
	readback, err := a.CloudControlAction.Readback(ctx, request)
	if err != nil || !readback.Exists {
		return readback, err
	}
	outcome, err := a.check(ctx, request)
	if err != nil {
		return contracts.ReadbackResult{}, err
	}
	if outcome.pending {
		return contracts.ReadbackResult{Exists: false, State: "deletion_scheduled", Data: outcome.evidence}, nil
	}
	return readback, nil
}

func (*guardedAction) DeletionCheckTimeout() time.Duration { return time.Hour }

// KMS keys cannot be deleted immediately: deletion is scheduled for a waiting
// period, and AWS managed keys cannot be scheduled at all.
func kmsKeyGuard(client KMSNativeAPI) deleteGuard {
	return func(ctx context.Context, keyID string) (guardOutcome, error) {
		output, err := client.DescribeKey(ctx, &awskms.DescribeKeyInput{KeyId: awssdk.String(keyID)})
		if nativeNotFound(err, "NotFoundException") {
			return guardOutcome{pending: true, evidence: map[string]any{"key_state": "absent"}}, nil
		}
		if err != nil {
			return guardOutcome{}, NormalizeError(err)
		}
		if output.KeyMetadata == nil || awssdk.ToString(output.KeyMetadata.KeyId) == "" {
			return guardOutcome{}, fmt.Errorf("AWS KMS DescribeKey returned no key metadata")
		}
		metadata := output.KeyMetadata
		evidence := map[string]any{"key_state": string(metadata.KeyState), "key_manager": string(metadata.KeyManager)}
		if metadata.DeletionDate != nil {
			evidence["deletion_date"] = metadata.DeletionDate.UTC().Format(time.RFC3339)
		}
		switch {
		case metadata.KeyManager == kmstypes.KeyManagerTypeAws:
			return guardOutcome{blocked: "AWS managed KMS keys are owned by their service and cannot be deleted", evidence: evidence}, nil
		case metadata.KeyState == kmstypes.KeyStatePendingDeletion || metadata.KeyState == kmstypes.KeyStatePendingReplicaDeletion:
			return guardOutcome{pending: true, evidence: evidence}, nil
		}
		return guardOutcome{evidence: evidence}, nil
	}
}

// A backup vault can only be deleted when it holds no recovery points, and a
// vault lock in compliance mode prevents deletion after its grace time.
func backupVaultGuard(client BackupNativeAPI) deleteGuard {
	return func(ctx context.Context, name string) (guardOutcome, error) {
		output, err := client.DescribeBackupVault(ctx, &awsbackup.DescribeBackupVaultInput{BackupVaultName: awssdk.String(name)})
		if nativeNotFound(err, "ResourceNotFoundException") {
			return guardOutcome{pending: true, evidence: map[string]any{"vault_state": "absent"}}, nil
		}
		if err != nil {
			return guardOutcome{}, NormalizeError(err)
		}
		if !strings.EqualFold(awssdk.ToString(output.BackupVaultName), name) {
			return guardOutcome{}, fmt.Errorf("AWS Backup DescribeBackupVault returned another vault")
		}
		points := output.NumberOfRecoveryPoints
		evidence := map[string]any{"recovery_points": points, "locked": awssdk.ToBool(output.Locked)}
		if output.LockDate != nil {
			evidence["lock_date"] = output.LockDate.UTC().Format(time.RFC3339)
		}
		if points > 0 {
			return guardOutcome{blocked: fmt.Sprintf("the backup vault still contains %d recovery points", points), evidence: evidence}, nil
		}
		if awssdk.ToBool(output.Locked) && output.LockDate != nil && time.Now().After(*output.LockDate) {
			return guardOutcome{blocked: "the backup vault is locked in compliance mode and cannot be deleted", evidence: evidence}, nil
		}
		return guardOutcome{evidence: evidence}, nil
	}
}
