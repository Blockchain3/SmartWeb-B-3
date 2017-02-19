package maker

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/eris-ltd/eris/definitions"
	"github.com/eris-ltd/eris/keys"
	"github.com/eris-ltd/eris/log"

	"github.com/eris-ltd/eris-db/genesis"
	ptypes "github.com/eris-ltd/eris-db/permission/types"
)

// ErisDBAccountConstructor contains different views on a single account
// for the purpose of constructing the configuration, genesis, and private
// validator file.
// Note that the generation of key pairs for the private validator is only
// for development purposes and that under
type ErisDBAccountConstructor struct {
	genesisAccount          *genesis.GenesisAccount          `json:"genesis_account"`
	genesisValidator        *genesis.GenesisValidator        `json:"genesis_validator"`
	genesisPrivateValidator *genesis.GenesisPrivateValidator `json:"genesis_private_validator"`

	// NOTE: [ben] this is redundant information to preserve the current behaviour of
	// tooling to write the untyped public key for all accounts in accounts.csv
	untypedPublicKeyBytes []byte
	typeBytePublicKey     byte

	// NOTE: [ben] this is redundant information and unsafe but is put in place to
	// temporarily preserve the behaviour that the private keys of a *development*
	// chain can be written to the host
	// NOTE: [ben] because this is bad practice, it now requires explicit
	// flag `eris chains make --unsafe` (unsafe bool in signatures below)
	untypedPrivateKeyBytes []byte
}

// MakeAccounts specifies the chaintype and chain name and creates the constructors for generating
// configuration, genesis and private validator files (the latter if required - for development purposes)
// NOTE: [ben] if unsafe is set to true the private keys will be extracted from eris-keys and be written
// into accounts.json. This will be deprecated in v0.17
func MakeAccounts(name, chainType string, accountTypes []*definitions.ErisDBAccountType, unsafe bool) ([]*ErisDBAccountConstructor, error) {

	accountConstructors := []*ErisDBAccountConstructor{}

	switch chainType {
	// NOTE: [ben] "mint" is a legacy differentiator that refers to the consensus engine that eris-db uses
	// and currently Tendermint is the only consensus engine (chain) that is supported.  As such the variable
	// "chainType" can be misleading.
	case "mint":
		for _, accountType := range accountTypes {
			log.WithField("type", accountType.Name).Info("Making Account Type")
			for i := 0; i < accountType.Number; i++ {
				// account names are formatted <ChainName_AccountTypeName_nnn>
				accountName := strings.ToLower(fmt.Sprintf(
					"%s_%s_%03d", name, accountType.Name, i))
				log.WithField("name", accountName).Debug("Making Account")

				// NOTE: [ben] for v0.16 we get the private validator file if `ToBond` > 0
				// For v0.17 we will default to all validators only using remote signing,
				// and then we should block by default extraction of private validator file.
				// NOTE: [ben] currently we default to ed25519/SHA512 for PKI and ripemd16
				// for address calculation.
				accountConstructor, err := newErisDBAccountConstructor(accountName, "ed25519,ripemd160",
					accountType, false, unsafe)
				if err != nil {
					return nil, fmt.Errorf("Failed to construct account %s for %s", accountName, name)
				}
				// add the account constructor to the return slice
				accountConstructors = append(accountConstructors, accountConstructor)
			}
		}
		return accountConstructors, nil
	default:
		return nil, fmt.Errorf("Unknown chain type specifier (chainType: %s)", chainType)
	}
}

//-----------------------------------------------------------------------------------------------------
// helper functions for MakeAccounts

// newErisDBAccountConstructor returns an ErisDBAccountConstructor that has a GenesisAccount
// and depending on the AccountType returns a GenesisValidator.  If a private validator file
// is needed for a validating account, it will pull the private key, unless this is
// explicitly blocked.
func newErisDBAccountConstructor(accountName string, keyAddressType string,
	accountType *definitions.ErisDBAccountType, blockPrivateValidator, unsafe bool) (*ErisDBAccountConstructor, error) {

	var err error
	isValidator := (accountType.ToBond > 0 && accountType.Tokens >= accountType.ToBond)
	accountConstructor := &ErisDBAccountConstructor{}
	var genesisPrivateValidator *genesis.GenesisPrivateValidator
	permissions := &ptypes.AccountPermissions{}
	// TODO: expose roles
	// convert the permissions map of string-integer pairs to an
	// AccountPermissions type.
	if permissions, err = ptypes.ConvertPermissionsMapAndRolesToAccountPermissions(
		accountType.Perms, []string{}); err != nil {
		return nil, err
	}
	var address, publicKeyBytes []byte
	switch keyAddressType {
	// use ed25519/SHA512 for PKI and ripemd160 for Address
	case "ed25519,ripemd160":
		if address, publicKeyBytes, genesisPrivateValidator, err = generateAddressAndKey(
			keyAddressType, blockPrivateValidator); err != nil {
			return nil, err
		}

		// NOTE: [ben] these auxiliary fields in the constructor are to be deprecated
		// but introduced to support current unsafe behaviour where all private keys
		// are extracted from eris-keys
		copy(accountConstructor.untypedPublicKeyBytes, publicKeyBytes)
		// tendermint/go-crypto typebyte for ed25519
		accountConstructor.typeBytePublicKey = byte(0x01)

		if unsafe {
			copy(accountConstructor.untypedPrivateKeyBytes, genesisPrivateValidator.PrivKey.Bytes())
		}
	default:
		// the other code paths in eris-keys are currently not tested for;
		return nil, fmt.Errorf("Currently only supported ed265519/ripemd160: unknown key type (%s)",
			keyAddressType)
	}

	accountConstructor.genesisAccount = genesis.NewGenesisAccount(
		// Genesis address
		address,
		// Genesis amount
		int64(accountType.Tokens),
		// Genesis name
		accountName,
		// Genesis permissions
		permissions)

	// Define this account as a bonded validator in genesis.
	if isValidator {
		accountConstructor.genesisValidator, err = genesis.NewGenesisValidator(
			// Genesis validator amount
			int64(accountType.Tokens),
			// Genesis validator name
			accountName,
			// Genesis validator unbond to address
			address,
			// Genesis validator bond amount
			int64(accountType.ToBond),
			// Genesis validator public key type string
			// Currently only ed22519 is exposed through the tooling
			"ed25519",
			// Genesis validator public key bytes
			publicKeyBytes)
		if err != nil {
			return nil, err
		}

		if genesisPrivateValidator != nil && !blockPrivateValidator {
			// explicitly copy genesis private validator for clarity
			accountConstructor.genesisPrivateValidator = genesisPrivateValidator			
		}
	}

	return accountConstructor, nil
}

//----------------------------------------------------------------------------------------------------
// helper functions with eris-keys

// generateAddressAndKey returns an address, public key and if requested the JSON bytes of a
// private validator structure.
func generateAddressAndKey(keyAddressType string, blockPrivateValidator bool) (address []byte, publicKey []byte,
	genesisPrivateValidator *genesis.GenesisPrivateValidator, err error) {
	addressString, publicKeyString, privateValidatorJson, err := makeKey(keyAddressType, blockPrivateValidator)
	if err != nil {
		return
	}

	if address, err = hex.DecodeString(addressString); err != nil {
		return
	}

	if publicKey, err = hex.DecodeString(publicKeyString); err != nil {
		return
	}

	if !blockPrivateValidator {
		// TODO: [ben] check that empty byte slice returns error and does not unmarshal into
		// zero GenesisPrivateValidator type
		if err = json.Unmarshal(privateValidatorJson, genesisPrivateValidator); err != nil {
			log.Error(string(privateValidatorJson))
			return
		}
	}

	return
}

// ugh. TODO: further clean up eris-keys.
func makeKey(keyType string, blockPrivateValidator bool) (address string, publicKey string, privateValidatorJson []byte, err error) {
	log.WithFields(log.Fields{
		"type": keyType,
	}).Debug("Sending Call to eris-keys server")

	keyClient, err := keys.InitKeyClient()
	if err != nil {
		return
	}

	// note, for now we use no password to lock/unlock keys
	if address, err = keyClient.GenerateKey(false, true, keyType, ""); err != nil {
		return
	}

	if publicKey, err = keyClient.PubKey(address, ""); err != nil {
		return
	}

	if !blockPrivateValidator {
		if privateValidatorJson, err = keyClient.Convert(address, ""); err != nil {
			return
		}
	} else {
		privateValidatorJson = []byte{}
	}

	return
}
