package deposit

import (
    "bytes"
    "encoding/hex"
    "errors"

    "github.com/prysmaticlabs/go-ssz"

    "github.com/rocket-pool/smartnode/shared/services"
    "github.com/rocket-pool/smartnode/shared/utils/eth"
)


// Deposit amount in gwei
const DEPOSIT_AMOUNT uint64 = 32000000000


// DepositData data
type DepositData struct {
    Pubkey [48]byte
    WithdrawalCredentials [32]byte
    Amount uint64
    Signature [96]byte
}


// Deposit reservation response type
type DepositReserveResponse struct {
    Success bool                    `json:"success"`
    HasExistingReservation bool     `json:"hasExistingReservation"`
    DepositsEnabled bool            `json:"depositsEnabled"`
    PubkeyUsed bool                 `json:"pubkeyUsed"`
}


// Reserve node deposit
func ReserveDeposit(p *services.Provider, durationId string) (*DepositReserveResponse, error) {

    // Response
    response := &DepositReserveResponse{}

    // Generate new validator key
    key, err := p.KM.CreateValidatorKey()
    if err != nil {
        return nil, errors.New("Error generating validator key: " + err.Error())
    }
    pubkey := key.PublicKey.Marshal()

    // Status channels
    hasReservationChannel := make(chan bool)
    depositsAllowedChannel := make(chan bool)
    pubkeyUsedChannel := make(chan bool)
    errorChannel := make(chan error)

    // Check node does not have current deposit reservation
    go (func() {
        hasReservation := new(bool)
        if err := p.NodeContract.Call(nil, hasReservation, "getHasDepositReservation"); err != nil {
            errorChannel <- errors.New("Error retrieving deposit reservation status: " + err.Error())
        } else {
            hasReservationChannel <- *hasReservation
        }
    })()

    // Check node deposits are enabled
    go (func() {
        depositsAllowed := new(bool)
        if err := p.CM.Contracts["rocketNodeSettings"].Call(nil, depositsAllowed, "getDepositAllowed"); err != nil {
            errorChannel <- errors.New("Error checking node deposits enabled status: " + err.Error())
        } else {
            depositsAllowedChannel <- *depositsAllowed
        }
    })()

    // Check pubkey is not in use
    go (func() {
        pubkeyUsedKey := eth.KeccakBytes(bytes.Join([][]byte{[]byte("validator.pubkey.used"), pubkey}, []byte{}))
        if pubkeyUsed, err := p.CM.RocketStorage.GetBool(nil, pubkeyUsedKey); err != nil {
            errorChannel <- errors.New("Error retrieving pubkey used status: " + err.Error())
        } else {
            pubkeyUsedChannel <- pubkeyUsed
        }
    })()

    // Receive status
    for received := 0; received < 3; {
        select {
            case response.HasExistingReservation = <- hasReservationChannel:
                received++
            case response.DepositsEnabled = <-depositsAllowedChannel:
                received++
            case response.PubkeyUsed = <- pubkeyUsedChannel:
                received++
            case err := <-errorChannel:
                return nil, err
        }
    }

    // Check status
    if response.HasExistingReservation || !response.DepositsEnabled || response.PubkeyUsed {
        return response, nil
    }

    // Get RP withdrawal pubkey
    // :TODO: replace with correct withdrawal pubkey once available
    withdrawalPubkeyHex := []byte("0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef")
    withdrawalPubkey := make([]byte, hex.DecodedLen(len(withdrawalPubkeyHex)))
    _,_ = hex.Decode(withdrawalPubkey, withdrawalPubkeyHex)

    // Build withdrawal credentials
    withdrawalCredentials := eth.KeccakBytes(withdrawalPubkey) // Withdrawal pubkey hash
    withdrawalCredentials[0] = 0 // Replace first byte with BLS_WITHDRAWAL_PREFIX_BYTE

    // Build DepositData object
    depositData := &DepositData{}
    copy(depositData.Pubkey[:], pubkey)
    copy(depositData.WithdrawalCredentials[:], withdrawalCredentials[:])
    depositData.Amount = DEPOSIT_AMOUNT

    // Build signature
    signingRoot, err := ssz.SigningRoot(depositData)
    if err != nil {
        return nil, errors.New("Error retrieving deposit data hash tree root: " + err.Error())
    }
    signature := key.SecretKey.Sign(signingRoot[:]).Marshal()

    // Create deposit reservation
    if txor, err := p.AM.GetNodeAccountTransactor(); err != nil {
        return nil, err
    } else {
        if _, err := eth.ExecuteContractTransaction(p.Client, txor, p.NodeContractAddress, p.CM.Abis["rocketNodeContract"], "depositReserve", durationId, pubkey, signature); err != nil {
            return nil, errors.New("Error making deposit reservation: " + err.Error())
        } else {
            response.Success = true
        }
    }

    // Return response
    return response, nil

}

