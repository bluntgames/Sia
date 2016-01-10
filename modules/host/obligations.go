package host

import (
	"os"
	"sync"

	"github.com/NebulousLabs/Sia/build"
	"github.com/NebulousLabs/Sia/modules"
	"github.com/NebulousLabs/Sia/types"
)

const (
	// resubmissionTimeout is the number of blocks that the host will wait
	// before trying to resubmit a transaction to the blockchain.
	resubmissionTimeout = 2

	// Booleans to indicate that a contract obligation has been successful or
	// unsuccessful.
	obligationSucceeded = true
	obligationFailed    = false
)

var (
	// confirmationRequirement is the number of blocks that the host waits
	// before assuming that a storage proof has been confirmed by the
	// blockchain, and will not need to be reconstructed.
	confirmationRequirement = func() types.BlockHeight {
		if build.Release == "testing" {
			return 3
		}
		if build.Release == "standard" {
			return 12
		}
		if build.Release == "dev" {
			return 6
		}
		panic("unrecognized release value")
	}()
)

// A contractObligation tracks a file contract that the host is obligated to
// fulfill.
type contractObligation struct {
	// Blockchain tracking. The storage proof transaction is not tracked
	// because the same storage proof transaction is not always guaranteed to
	// be valid. If the origin or the revision has not been confirmed, the host
	// will need to resubmit them to the transaction pool.
	ID                  types.FileContractID // The ID of the file contract.
	OriginTransaction   types.Transaction    // The transaction containing the original file contract.
	RevisionTransaction types.Transaction    // The most recent revision to the contract.
	OriginConfirmed     bool                 // whether the origin transaction has been confirmed.
	RevisionConfirmed   bool                 // whether the most recent revision has been confirmed.
	ProofConfirmed      bool                 // whether the storage proof has been confirmed.

	// Where on disk the file is stored.
	Path string

	// The mutex ensures that revisions are happening in serial. The actual
	// data under the obligations is being protected by the host's mutex.
	// Grabbing 'mu' is not sufficient to guarantee modification safety of the
	// struct, the host mutex must also be grabbed.
	mu sync.Mutex
}

// fileSize returns the size of the file that is held by the contract
// obligation.
func (co *contractObligation) fileSize() uint64 {
	if co.hasRevision() {
		return co.RevisionTransaction.FileContractRevisions[0].NewFileSize
	}
	return co.OriginTransaction.FileContracts[0].FileSize
}

// hasRevision indiciates whether there is a file contract reivision contained
// within the contract obligation.
func (co *contractObligation) hasRevision() bool {
	return len(co.RevisionTransaction.FileContractRevisions) == 1
}

// missedProofUnlockHash returns the operating unlock hash for a successful
// file contract in the obligation.
func (co *contractObligation) missedProofUnlockHash() types.UnlockHash {
	if co.hasRevision() {
		return co.RevisionTransaction.FileContractRevisions[0].NewMissedProofOutputs[1].UnlockHash
	}
	return co.OriginTransaction.FileContracts[0].MissedProofOutputs[1].UnlockHash
}

// payout returns the operating payout of the contract obligation.
func (co *contractObligation) payout() types.Currency {
	// Function seems unnecessary because it is just a getter, but adding this
	// function helps maintain consistency with the way that the other fields
	// are accessed.
	return co.OriginTransaction.FileContracts[0].Payout
}

// proofConfirmed inidicates whether the storage proofs have been seen on the
// blockchain.
func (co *contractObligation) proofConfirmed() bool {
	// Function seems unnecessary because it is just a getter, but adding this
	// function helps maintain consistency with the way that the other fields
	// are accessed.
	return co.ProofConfirmed
}

// reset updates the contract obligation to reflect that the consensus set is
// being rescanned, which means all of the consensus indicators need to be
// reset, and the action items need to be filled out again.
func (co *contractObligation) reset() {
	co.OriginConfirmed = false
	co.ProofConfirmed = false

	// If there is no revision, then the final revision counts as being
	// confirmed. Otherwise, the revision should not be considered as
	// confirmed.
	co.RevisionConfirmed = !co.hasRevision()
}

// revisionNumber returns the operating revision number of the obligation.
func (co *contractObligation) revisionNumber() uint64 {
	if co.hasRevision() {
		return co.RevisionTransaction.FileContractRevisions[0].NewRevisionNumber
	}
	return co.OriginTransaction.FileContracts[0].RevisionNumber
}

// txnsConfirmed indicates whether the file contract and its latest revision
// have been seen on the blockchain.
func (co *contractObligation) txnsConfirmed() bool {
	// The origin transaction is always present. If the origin transaction
	// isn't marked as confirmed, then the transactions aren't confirmed.
	if !co.OriginConfirmed {
		return false
	}

	// If there is a revision and it hasn't been confirmed, then the
	// transactions aren't confirmed.
	if co.hasRevision() && !co.RevisionConfirmed {
		return false
	}

	// Both potential fail conditions have been checked, all other possibilites
	// mean that all transactions have been confirmed.
	return true
}

// validProofUnlockHash returns the operating unlock hash for a successful file
// contract in the obligation.
func (co *contractObligation) validProofUnlockHash() types.UnlockHash {
	if co.hasRevision() {
		return co.RevisionTransaction.FileContractRevisions[0].NewValidProofOutputs[1].UnlockHash
	}
	return co.OriginTransaction.FileContracts[0].ValidProofOutputs[1].UnlockHash
}

// value returns the expected monetary value of the file contract.
func (co *contractObligation) value() types.Currency {
	if co.hasRevision() {
		return co.RevisionTransaction.FileContractRevisions[0].NewValidProofOutputs[1].Value
	}
	return co.OriginTransaction.FileContracts[0].ValidProofOutputs[1].Value
}

// unlockHash returns the operating unlock hash of the contract obligation.
func (co *contractObligation) unlockHash() types.UnlockHash {
	if co.hasRevision() {
		return co.RevisionTransaction.FileContractRevisions[0].NewUnlockHash
	}
	return co.OriginTransaction.FileContracts[0].UnlockHash
}

// windowStart returns the first block in the storage proof window of the
// contract obligation.
func (co *contractObligation) windowStart() types.BlockHeight {
	if co.hasRevision() {
		return co.RevisionTransaction.FileContractRevisions[0].NewWindowStart
	}
	return co.OriginTransaction.FileContracts[0].WindowStart
}

// windowEnd returns the first block in the storage proof window of the
// contract obligation.
func (co *contractObligation) windowEnd() types.BlockHeight {
	if co.hasRevision() {
		return co.RevisionTransaction.FileContractRevisions[0].NewWindowEnd
	}
	return co.OriginTransaction.FileContracts[0].WindowEnd
}

// addObligation adds a new file contract obligation to the host. The
// obligation assumes that none of the transaction required by the obligation
// have not yet been confirmed on the blockchain.
func (h *Host) addObligation(co *contractObligation) {
	// 'addObligation' should not be adding an obligation that has a revision.
	if build.DEBUG && co.hasRevision() {
		panic("calling 'addObligation' with a file contract revision")
	}

	// Add the obligation to the list of host obligations.
	h.obligationsByID[co.ID] = co

	// The host needs to verify that the obligation transaction made it into
	// the blockchain.
	h.addActionItem(h.blockHeight+resubmissionTimeout, co)

	// Update the statistics.
	h.anticipatedRevenue = h.anticipatedRevenue.Add(co.value()) // Output at index 1 alone belongs to host.
	h.spaceRemaining = h.spaceRemaining - int64(co.fileSize())

	err := h.save()
	if err != nil {
		h.log.Println("WARN: failed to save host:", err)
	}
}

// reviseObligation takes a file contract revision + transaction and applies it
// to an existing obligation.
func (h *Host) reviseObligation(revisionTransaction types.Transaction) {
	// Sanity check - there should only be one file contract revision in the
	// transaction.
	if build.DEBUG && len(revisionTransaction.FileContractRevisions) != 1 {
		panic("cannot revise obligation without a file contract revision")
	}
	obligation, exists := h.obligationsByID[revisionTransaction.FileContractRevisions[0].ParentID]
	if build.DEBUG && !exists {
		panic("cannot revise obligation - obligation not found")
	}

	// Update the host's statistics.
	h.spaceRemaining += int64(obligation.fileSize())
	h.spaceRemaining -= int64(revisionTransaction.FileContractRevisions[0].NewFileSize)
	h.anticipatedRevenue = h.anticipatedRevenue.Sub(obligation.value())
	h.anticipatedRevenue = h.anticipatedRevenue.Add(revisionTransaction.FileContractRevisions[0].NewValidProofOutputs[1].Value)

	// The host needs to verify that the revision transaction made it into the
	// blockchain.
	h.addActionItem(h.blockHeight+resubmissionTimeout, obligation)

	// Add the revision to the obligation
	obligation.RevisionTransaction = revisionTransaction
	obligation.RevisionConfirmed = false
}

// removeObligation removes a file contract obligation and the corresponding
// file, allowing that space to be reallocated to new file contracts.
//
// TODO: The error handling in this function is not very tolerant.
func (h *Host) removeObligation(co *contractObligation, successful bool) {
	// Get the size of the file that's about to be removed.
	var size int64
	stat, err := os.Stat(co.Path)
	if err != nil {
		h.log.Println("WARN: failed to remove obligation:", err)
	} else {
		size = stat.Size()
	}

	// Remove the file and reallocate the space. If any of the operations fail,
	// none of the space will be re-added.
	err = os.Remove(co.Path)
	if err != nil {
		h.log.Println("WARN: failed to remove obligation:", err)
	} else {
		h.spaceRemaining += size
	}

	// Update host statistics.
	h.anticipatedRevenue = h.anticipatedRevenue.Sub(co.value())
	if successful {
		h.revenue = h.revenue.Add(co.value())
	} else {
		h.lostRevenue = h.lostRevenue.Add(co.value())
	}

	// Remove the obligation from memory.
	delete(h.obligationsByID, co.ID)
	err = h.save()
	if err != nil {
		h.log.Println("WARN: failed to save host:", err)
	}
}
