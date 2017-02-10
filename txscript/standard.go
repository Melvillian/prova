// Copyright (c) 2013-2015 The btcsuite developers
// Use of this source code is governed by an ISC
// license that can be found in the LICENSE file.

package txscript

import (
	"github.com/bitgo/rmgd/btcec"
	"github.com/bitgo/rmgd/chaincfg"
	"github.com/bitgo/rmgd/rmgutil"
)

const (
	// MaxDataCarrierSize is the maximum number of bytes allowed in pushed
	// data to be considered a nulldata transaction
	MaxDataCarrierSize = 80

	// StandardVerifyFlags are the script flags which are used when
	// executing transaction scripts to enforce additional checks which
	// are required for the script to be considered standard.  These checks
	// help reduce issues related to transaction malleability as well as
	// allow pay-to-script hash transactions.  Note these flags are
	// different than what is required for the consensus rules in that they
	// are more strict.
	//
	// TODO: This definition does not belong here.  It belongs in a policy
	// package.
	StandardVerifyFlags = ScriptBip16 |
		ScriptVerifyDERSignatures |
		ScriptVerifyStrictEncoding |
		ScriptVerifyMinimalData |
		ScriptStrictMultiSig |
		ScriptDiscourageUpgradableNops |
		ScriptVerifyCleanStack |
		ScriptVerifyCheckLockTimeVerify |
		ScriptVerifyLowS
)

// ScriptClass is an enumeration for the list of standard types of script.
type ScriptClass byte

// Classes of script payment known about in the blockchain.
const (
	NonStandardTy  ScriptClass = iota // None of the recognized forms.
	PubKeyTy                          // Pay pubkey.
	PubKeyHashTy                      // Pay pubkey hash.
	ScriptHashTy                      // Pay to script hash.
	MultiSigTy                        // Multi signature.
	NullDataTy                        // Empty data-only (provably prunable).
	AztecTy                           // Aztec standard 2-of-3 type (subset of GeneralAztecTy)
	GeneralAztecTy                    // Aztec (generalized m-of-n) script
	AztecAdminTy                      // Aztec Admin Operations
)

// scriptClassToName houses the human-readable strings which describe each
// script class.
var scriptClassToName = []string{
	// TODO(aztec): clean up non-used types
	NonStandardTy:  "nonstandard",
	PubKeyTy:       "pubkey",
	PubKeyHashTy:   "pubkeyhash",
	ScriptHashTy:   "scripthash",
	MultiSigTy:     "multisig",
	NullDataTy:     "nulldata",
	AztecTy:        "aztec",
	GeneralAztecTy: "aztec",
	AztecAdminTy:   "aztecadmin",
}

// String implements the Stringer interface by returning the name of
// the enum script class. If the enum is invalid then "Invalid" will be
// returned.
func (t ScriptClass) String() string {
	if int(t) > len(scriptClassToName) || int(t) < 0 {
		return "Invalid"
	}
	return scriptClassToName[t]
}

// isGeneralAztec returns true if the passed script is an Aztec script (generalized m-of-n)
func isGeneralAztec(pops []parsedOpcode) bool {
	// The absolute minimum is 3 keys:
	// OP_2 <keyid> <keyid> <pubkey> OP_3 OP_CHECKMULTISIG
	sLen := len(pops)
	if sLen < 6 {
		return false
	}
	if !isSmallInt(pops[0].opcode) {
		return false
	}
	if !isSmallInt(pops[sLen-2].opcode) {
		return false
	}
	if pops[sLen-1].opcode.value != OP_CHECKSAFEMULTISIG {
		return false
	}

	// Get the number of sigs and keys for further validation
	nSigs := asSmallInt(pops[0].opcode)
	nKeys := asSmallInt(pops[sLen-2].opcode)

	// No effective single-sig allowed
	if nSigs < 2 {
		return false
	}

	// Verify the number of pubkeys specified matches the actual number
	// of pubkeys provided.
	if sLen-2-1 != nKeys {
		return false
	}

	// Run through the key ids and key hashes, counting each
	// Also, require that key ids come first, followed by key hashes.
	nKeyIDs := 0
	nKeyHashes := 0
	seenKeyIDs := make(map[int32]bool)
	for _, pop := range pops[1 : sLen-2] {
		dataLen := len(pop.data)
		// Valid pubkey hashes are 20 bytes
		if dataLen == 20 {
			// Key hashes MUST come before any key ids
			if nKeyIDs > 0 {
				return false
			}
			nKeyHashes++
		} else if isUint32(pop.opcode) {
			// Otherwise, it should look like a KeyID
			keyID, err := asInt32(pop)
			if err != nil {
				return false
			}
			_, seen := seenKeyIDs[keyID]
			if seen {
				// Duplicate key ids not allowed
				return false
			}
			seenKeyIDs[keyID] = true
			nKeyIDs++
		}
	}

	// Cannot allow raw key hashes to move funds without at least 1 KeyID
	if nKeyHashes >= nSigs {
		return false
	}

	// All key ids should be able to move funds in collaboration
	if nKeyIDs < nSigs {
		return false
	}

	return true
}

// isAztec returns true if the passed script is a 2 of 3 aztec transaction.
func isAztec(pops []parsedOpcode) bool {
	return len(pops) == 6 &&
		pops[0].opcode.value == OP_2 &&
		pops[4].opcode.value == OP_3 &&
		isGeneralAztec(pops)
}

// IsAztecTx determines if a transaction is a standard aztec transaction consisting
// of only outputs to standard aztec scripts and 0-value nulldata scripts.
func IsAztecTx(tx *rmgutil.Tx) bool {
	msgTx := tx.MsgTx()

	// An aztec transaction must have at least one output.
	if len(msgTx.TxOut) == 0 {
		return false
	}

	for _, txOut := range msgTx.TxOut {
		atoms := txOut.Value
		pops, err := ParseScript(txOut.PkScript)
		if err != nil {
			return false
		}
		if isNullData(pops) {
			if atoms != 0 {
				return false
			}
		} else if !isGeneralAztec(pops) {
			return false
		}
	}
	return true
}

// GetAdminDetails will read threadID and admin outputs from and admin transaction.
func GetAdminDetails(tx *rmgutil.Tx) (int, [][]parsedOpcode) {
	// The first output of the admin transaction is the thread transaction.
	// Additional outputs modify the chain state. We expect at least one additional.
	if len(tx.MsgTx().TxOut) < 1 {
		return -1, nil
	}
	pops, err := ParseScript(tx.MsgTx().TxOut[0].PkScript)
	if err != nil {
		return -1, nil
	}
	if TypeOfScript(pops) != AztecAdminTy {
		return -1, nil
	}
	threadID, err := ExtractThreadID(pops)
	if err != nil {
		return -1, nil
	}
	adminOutputs := make([][]parsedOpcode, len(tx.MsgTx().TxOut)-1)
	for i := 1; i < len(tx.MsgTx().TxOut); i++ {
		pops, err := ParseScript(tx.MsgTx().TxOut[i].PkScript)
		if err != nil {
			return -1, nil
		}
		adminOutputs[i-1] = pops
	}
	return int(threadID), adminOutputs
}

// isAztecAdmin returns true if the passed script is admin tread script.
func isAztecAdmin(pops []parsedOpcode) bool {
	sLen := len(pops)
	if sLen != 2 {
		return false
	}
	if pops[sLen-1].opcode.value != OP_CHECKTHREAD {
		return false
	}
	threadID := rmgutil.ThreadID(asSmallInt(pops[0].opcode))
	if threadID < rmgutil.RootThread || threadID > rmgutil.IssueThread {
		return false
	}
	return true
}

// IsValidAdminOp returns true if the passed script is a valid admin
// operation at the given thread.
func IsValidAdminOp(pops []parsedOpcode, threadID rmgutil.ThreadID) bool {
	// always expect two ops
	// <OP_RETURN><OP_DATA>
	if len(pops) != 2 {
		return false
	}
	if pops[0].opcode.value != OP_RETURN {
		return false
	}
	if pops[1].opcode.value != OP_DATA_34 &&
		pops[1].opcode.value != OP_DATA_38 {
		return false
	}
	// read op type byte and check valid key
	op, _, err := ExtractAdminData(pops)
	if err != nil {
		return false
	}
	// check thread specific operations
	switch threadID {
	case rmgutil.RootThread:
		if op == OP_PROVISIONINGKEYADD ||
			op == OP_PROVISIONINGKEYREVOKE ||
			op == OP_ISSUINGKEYADD ||
			op == OP_ISSUINGKEYREVOKE {
			return true
		}
	case rmgutil.ProvisionThread:
		if op == OP_VALIDATEKEYADD ||
			op == OP_VALIDATEKEYREVOKE {
			return true
		}
		if op == OP_WSPKEYADD ||
			op == OP_WSPKEYREVOKE {
			// check length of data for WSP ops
			if len(pops[1].data) == 1+btcec.PubKeyBytesLenCompressed+btcec.KeyIDSize {
				return true
			}
		}
	case rmgutil.IssueThread:
		return false
	}
	return false
}

// isNullData returns true if the passed script is a null data transaction,
// false otherwise.
func isNullData(pops []parsedOpcode) bool {
	// A nulldata transaction is either a single OP_RETURN or an
	// OP_RETURN SMALLDATA (where SMALLDATA is a data push up to
	// MaxDataCarrierSize bytes).
	l := len(pops)
	if l == 1 && pops[0].opcode.value == OP_RETURN {
		return true
	}

	return l == 2 &&
		pops[0].opcode.value == OP_RETURN &&
		pops[1].opcode.value <= OP_PUSHDATA4 &&
		len(pops[1].data) <= MaxDataCarrierSize
}

// TypeOfScript returns the type of the script being inspected from the known
// standard types.
func TypeOfScript(pops []parsedOpcode) ScriptClass {
	return typeOfScript(pops)
}

// typeOfScript returns the type of the script being inspected from the known
// standard types.
func typeOfScript(pops []parsedOpcode) ScriptClass {
	if isNullData(pops) {
		return NullDataTy
	} else if isAztec(pops) {
		return AztecTy
	} else if isGeneralAztec(pops) {
		return GeneralAztecTy
	} else if isAztecAdmin(pops) {
		return AztecAdminTy
	}
	return NonStandardTy
}

// GetScriptClass returns the class of the script passed.
//
// NonStandardTy will be returned when the script does not parse.
func GetScriptClass(script []byte) ScriptClass {
	pops, err := ParseScript(script)
	if err != nil {
		return NonStandardTy
	}
	return typeOfScript(pops)
}

// expectedInputs returns the number of arguments required by a script.
// If the script is of unknown type such that the number can not be determined
// then -1 is returned. We are an internal function and thus assume that class
// is the real class of pops (and we can thus assume things that were determined
// while finding out the type).
func expectedInputs(pops []parsedOpcode, class ScriptClass) int {
	switch class {
	case AztecTy:
		fallthrough
	case GeneralAztecTy:
		// Standard Aztec script first push is a small number for the number
		// of (sig, pubkey) pairs. Unlike multisig Bitcoin scripts, Aztec
		// scripts use key hashes rather than keys, thus the keys must be
		// included on redemption.
		return asSmallInt(pops[0].opcode) * 2

	case AztecAdminTy:
		//2 pubkeys + 2 sigs
		return 4

	case NullDataTy:
		fallthrough
	default:
		return -1
	}
}

// ScriptInfo houses information about a script pair that is determined by
// CalcScriptInfo.
type ScriptInfo struct {
	// PkScriptClass is the class of the public key script and is equivalent
	// to calling GetScriptClass on it.
	PkScriptClass ScriptClass

	// NumInputs is the number of inputs provided by the public key script.
	NumInputs int

	// ExpectedInputs is the number of outputs required by the signature
	// script and any pay-to-script-hash scripts. The number will be -1 if
	// unknown.
	ExpectedInputs int

	// SigOps is the number of signature operations in the script pair.
	SigOps int
}

// CalcScriptInfo returns a structure providing data about the provided script
// pair.  It will error if the pair is in someway invalid such that they can not
// be analysed, i.e. if they do not parse or the pkScript is not a push-only
// script
func CalcScriptInfo(sigScript, pkScript []byte, bip16 bool) (*ScriptInfo, error) {
	sigPops, err := ParseScript(sigScript)
	if err != nil {
		return nil, err
	}

	pkPops, err := ParseScript(pkScript)
	if err != nil {
		return nil, err
	}

	// Push only sigScript makes little sense.
	si := new(ScriptInfo)
	si.PkScriptClass = typeOfScript(pkPops)

	// Can't have a pkScript that doesn't just push data.
	if !isPushOnly(sigPops) {
		return nil, ErrStackNonPushOnly
	}

	si.ExpectedInputs = expectedInputs(pkPops, si.PkScriptClass)

	// All entries pushed to stack (or are OP_RESERVED and exec will fail).
	si.NumInputs = len(sigPops)

	// Count sigops taking into account pay-to-script-hash.
	if si.PkScriptClass == ScriptHashTy && bip16 {
		// The pay-to-hash-script is the final data push of the
		// signature script.
		script := sigPops[len(sigPops)-1].data
		shPops, err := ParseScript(script)
		if err != nil {
			return nil, err
		}

		shInputs := expectedInputs(shPops, typeOfScript(shPops))
		if shInputs == -1 {
			si.ExpectedInputs = -1
		} else {
			si.ExpectedInputs += shInputs
		}
		si.SigOps = getSigOpCount(shPops, true)
	} else {
		si.SigOps = getSigOpCount(pkPops, true)
	}

	return si, nil
}

// CalcMultiSigStats returns the number of public keys and signatures from
// a multi-signature transaction script.  The passed script MUST already be
// known to be a multi-signature script.
func CalcMultiSigStats(script []byte) (int, int, error) {
	pops, err := ParseScript(script)
	if err != nil {
		return 0, 0, err
	}

	// A multi-signature script is of the pattern:
	//  NUM_SIGS PUBKEY PUBKEY PUBKEY... NUM_PUBKEYS OP_CHECKMULTISIG
	// Therefore the number of signatures is the oldest item on the stack
	// and the number of pubkeys is the 2nd to last.  Also, the absolute
	// minimum for a multi-signature script is 1 pubkey, so at least 4
	// items must be on the stack per:
	//  OP_1 PUBKEY OP_1 OP_CHECKMULTISIG
	if len(pops) < 4 {
		return 0, 0, ErrStackUnderflow
	}

	numSigs := asSmallInt(pops[0].opcode)
	numPubKeys := asSmallInt(pops[len(pops)-2].opcode)
	return numPubKeys, numSigs, nil
}

// payToAztecScript creates a new script to pay a transaction output to an
// Aztec 2-of-3 address.
func payToAztecScript(pubKeyHash []byte, keyIDs []btcec.KeyID) ([]byte, error) {
	if len(keyIDs) != 2 {
		return nil, ErrBadNumRequired
	}
	return NewScriptBuilder().
		AddOp(OP_2). // 2 signatures required
		AddData(pubKeyHash).
		AddInt64(int64(keyIDs[0])).
		AddInt64(int64(keyIDs[1])).
		AddOp(OP_3). // 3 keys in total
		AddOp(OP_CHECKSAFEMULTISIG).
		Script()
}

// PayToAddrScript creates a new script to pay a transaction output to a the
// specified address.
func PayToAddrScript(addr rmgutil.Address) ([]byte, error) {
	switch addr := addr.(type) {
	case *rmgutil.AddressAztec:
		if addr == nil {
			return nil, ErrUnsupportedAddress
		}
		return payToAztecScript(addr.ScriptAddress(), addr.ScriptKeyIDs())
	}

	return nil, ErrUnsupportedAddress
}

// AztecThreadScript creates a new script to pay a transaction output to an
// Aztec Admin Thread.
func AztecThreadScript(threadID rmgutil.ThreadID) ([]byte, error) {
	return NewScriptBuilder().
		AddInt64(int64(threadID)).
		AddOp(OP_CHECKTHREAD).Script()
}

// MultiSigScript returns a valid script for a multisignature redemption where
// nrequired of the keys in pubkeys are required to have signed the transaction
// for success.  An ErrBadNumRequired will be returned if nrequired is larger
// than the number of keys provided.
func MultiSigScript(pubkeys []*rmgutil.AddressPubKey, nrequired int) ([]byte, error) {
	if len(pubkeys) < nrequired {
		return nil, ErrBadNumRequired
	}

	builder := NewScriptBuilder().AddInt64(int64(nrequired))
	for _, key := range pubkeys {
		builder.AddData(key.ScriptAddress())
	}
	builder.AddInt64(int64(len(pubkeys)))
	builder.AddOp(OP_CHECKMULTISIG)

	return builder.Script()
}

// PushedData returns an array of byte slices containing any pushed data found
// in the passed script.  This includes OP_0, but not OP_1 - OP_16.
func PushedData(script []byte) ([][]byte, error) {
	pops, err := ParseScript(script)
	if err != nil {
		return nil, err
	}

	var data [][]byte
	for _, pop := range pops {
		if pop.data != nil {
			data = append(data, pop.data)
		} else if pop.opcode.value == OP_0 {
			data = append(data, nil)
		}
	}
	return data, nil
}

// ExtractPkScriptAddrs returns the type of script, addresses and required
// signatures associated with the passed PkScript.  Note that it only works for
// 'standard' transaction script types.  Any data such as public keys which are
// invalid are omitted from the results.
func ExtractPkScriptAddrs(pkScript []byte, chainParams *chaincfg.Params) (ScriptClass, []rmgutil.Address, int, error) {
	var addrs []rmgutil.Address
	var requiredSigs int

	// No valid addresses or required signatures if the script doesn't
	// parse.
	pops, err := ParseScript(pkScript)
	if err != nil {
		return NonStandardTy, nil, 0, err
	}

	scriptClass := typeOfScript(pops)
	switch scriptClass {

	case AztecTy:
		requiredSigs = 2
		key0, err0 := asInt32(pops[2])
		key1, err1 := asInt32(pops[3])
		keyIDs := []btcec.KeyID{
			btcec.KeyID(key0),
			btcec.KeyID(key1),
		}
		addr, err := rmgutil.NewAddressAztec(pops[1].data, keyIDs, chainParams)
		if err == nil && err0 == nil && err1 == nil {
			addrs = append(addrs, addr)
		}

	case GeneralAztecTy:
		// TODO(aztec): define what to do for generalized aztec scripts

	case AztecAdminTy:
		requiredSigs = 2

	case NullDataTy:
		// Null data transactions have no addresses or required
		// signatures.

	case NonStandardTy:
		// Don't attempt to extract addresses or required signatures for
		// nonstandard transactions.
	}

	return scriptClass, addrs, requiredSigs, nil
}
