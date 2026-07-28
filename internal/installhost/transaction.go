package installhost

import (
	"context"
	"errors"
	"fmt"
	"net"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"docklane.local/docklane/internal/domain"
)

const (
	TrustProfileP11Kit     = "p11-kit"
	ResolverProfileSystemd = "systemd-resolved"
)

var sha256Pattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
var servicePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.@-]{0,254}$`)

type Contract struct {
	BaseDomain             string
	DNSService             string
	ResolverService        string
	TrustProfile           string
	ResolverProfile        string
	TrustAnchorPath        string
	TrustAnchorFingerprint string
}

type ServiceState struct {
	Active bool
}

type Backend interface {
	ServiceState(context.Context, string) (ServiceState, error)
	ValidateDNSConfiguration(context.Context) error
	RefreshTrustStore(context.Context, string) error
	StartService(context.Context, string) error
	RestartService(context.Context, string) error
	StopService(context.Context, string) error
	FlushResolverCache(context.Context, string) error
	LookupHost(context.Context, string) ([]string, error)
	VerifyTrustAnchor(context.Context, string, string) error
}

type FileRollback interface {
	Rollback() error
}

type serviceRecord struct {
	Name     string
	Prior    ServiceState
	Expected ServiceState
	Touched  bool
	Restored bool
}

type Transaction struct {
	backend    Backend
	files      FileRollback
	contract   Contract
	dns        serviceRecord
	resolver   serviceRecord
	rolledBack bool
}

func BuildContract(
	specification domain.InstallationSpecification,
	trustAnchorFingerprint string,
) (Contract, error) {
	if err := specification.Validate(); err != nil {
		return Contract{}, err
	}
	contract := Contract{
		BaseDomain:             specification.BaseDomain,
		DNSService:             specification.Host.DNSService,
		ResolverService:        specification.Host.ResolverService,
		TrustProfile:           specification.Host.TrustProfile,
		ResolverProfile:        specification.Host.ResolverProfile,
		TrustAnchorPath:        specification.PKI.TrustAnchorPath,
		TrustAnchorFingerprint: trustAnchorFingerprint,
	}
	if err := contract.Validate(); err != nil {
		return Contract{}, err
	}
	return contract, nil
}

func Apply(
	ctx context.Context,
	backend Backend,
	files FileRollback,
	contract Contract,
) (*Transaction, error) {
	if backend == nil {
		return nil, errors.New("host integration backend is required")
	}
	if files == nil {
		return nil, errors.New("staged file rollback is required")
	}
	if err := contract.Validate(); err != nil {
		return nil, err
	}
	dnsState, err := backend.ServiceState(ctx, contract.DNSService)
	if err != nil {
		return nil, fmt.Errorf("inspect DNS service: %w", err)
	}
	resolverState, err := backend.ServiceState(ctx, contract.ResolverService)
	if err != nil {
		return nil, fmt.Errorf("inspect resolver service: %w", err)
	}
	transaction := &Transaction{
		backend:  backend,
		files:    files,
		contract: contract,
		dns: serviceRecord{
			Name: contract.DNSService, Prior: dnsState, Expected: dnsState,
		},
		resolver: serviceRecord{
			Name:  contract.ResolverService,
			Prior: resolverState, Expected: resolverState,
		},
	}
	if err := backend.ValidateDNSConfiguration(ctx); err != nil {
		return nil, transaction.abort(fmt.Errorf(
			"validate managed dnsmasq configuration: %w",
			err,
		))
	}
	if err := backend.RefreshTrustStore(ctx, contract.TrustProfile); err != nil {
		return nil, transaction.abort(fmt.Errorf(
			"refresh managed trust store: %w",
			err,
		))
	}
	if err := backend.VerifyTrustAnchor(
		ctx,
		contract.TrustAnchorPath,
		contract.TrustAnchorFingerprint,
	); err != nil {
		return nil, transaction.abort(fmt.Errorf(
			"verify managed trust anchor: %w",
			err,
		))
	}
	if err := transaction.activate(ctx, &transaction.dns); err != nil {
		return nil, transaction.abort(err)
	}
	if err := transaction.activate(ctx, &transaction.resolver); err != nil {
		return nil, transaction.abort(err)
	}
	if err := backend.FlushResolverCache(
		ctx,
		contract.ResolverProfile,
	); err != nil {
		return nil, transaction.abort(fmt.Errorf(
			"flush managed resolver cache: %w",
			err,
		))
	}
	if err := verifyDNS(ctx, backend, contract.BaseDomain); err != nil {
		return nil, transaction.abort(err)
	}
	return transaction, nil
}

func (contract Contract) Validate() error {
	if !validDomain(contract.BaseDomain) {
		return fmt.Errorf("invalid host integration base domain %q", contract.BaseDomain)
	}
	for label, service := range map[string]string{
		"DNS service":      contract.DNSService,
		"resolver service": contract.ResolverService,
	} {
		if !servicePattern.MatchString(service) {
			return fmt.Errorf("%s is invalid", label)
		}
	}
	if contract.DNSService == contract.ResolverService {
		return errors.New("DNS and resolver services must be distinct")
	}
	if contract.TrustProfile != TrustProfileP11Kit {
		return fmt.Errorf("unsupported trust profile %q", contract.TrustProfile)
	}
	if contract.ResolverProfile != ResolverProfileSystemd {
		return fmt.Errorf(
			"unsupported resolver profile %q",
			contract.ResolverProfile,
		)
	}
	if !filepath.IsAbs(contract.TrustAnchorPath) ||
		filepath.Clean(contract.TrustAnchorPath) != contract.TrustAnchorPath {
		return errors.New("trust anchor path must be absolute and canonical")
	}
	if !sha256Pattern.MatchString(contract.TrustAnchorFingerprint) {
		return errors.New("trust anchor fingerprint must be lowercase SHA-256")
	}
	return nil
}

func (transaction *Transaction) activate(
	ctx context.Context,
	record *serviceRecord,
) error {
	record.Touched = true
	var err error
	if record.Prior.Active {
		err = transaction.backend.RestartService(ctx, record.Name)
	} else {
		err = transaction.backend.StartService(ctx, record.Name)
	}
	current, inspectErr := transaction.backend.ServiceState(ctx, record.Name)
	if inspectErr == nil {
		record.Expected = current
	}
	if err != nil {
		return fmt.Errorf("activate service %s: %w", record.Name, err)
	}
	if inspectErr != nil {
		return fmt.Errorf("verify service %s after activation: %w", record.Name, inspectErr)
	}
	if !current.Active {
		return fmt.Errorf("service %s is inactive after activation", record.Name)
	}
	return nil
}

func (transaction *Transaction) Rollback() error {
	if transaction == nil || transaction.backend == nil || transaction.files == nil {
		return errors.New("host integration transaction is required")
	}
	if transaction.rolledBack {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := transaction.verifyServiceDrift(ctx); err != nil {
		return err
	}
	if err := transaction.files.Rollback(); err != nil {
		return fmt.Errorf("restore managed host files: %w", err)
	}
	var rollbackErrors []error
	if err := transaction.backend.RefreshTrustStore(
		ctx,
		transaction.contract.TrustProfile,
	); err != nil {
		rollbackErrors = append(rollbackErrors, fmt.Errorf(
			"refresh restored trust store: %w",
			err,
		))
	}
	dnsValid := true
	if err := transaction.backend.ValidateDNSConfiguration(ctx); err != nil {
		dnsValid = false
		rollbackErrors = append(rollbackErrors, fmt.Errorf(
			"validate restored dnsmasq configuration: %w",
			err,
		))
	}
	if err := transaction.restoreService(ctx, &transaction.resolver, true); err != nil {
		rollbackErrors = append(rollbackErrors, err)
	}
	if dnsValid {
		if err := transaction.restoreService(ctx, &transaction.dns, true); err != nil {
			rollbackErrors = append(rollbackErrors, err)
		}
	}
	if transaction.resolver.Prior.Active {
		if err := transaction.backend.FlushResolverCache(
			ctx,
			transaction.contract.ResolverProfile,
		); err != nil {
			rollbackErrors = append(rollbackErrors, fmt.Errorf(
				"flush restored resolver cache: %w",
				err,
			))
		}
	}
	if len(rollbackErrors) != 0 {
		return errors.Join(rollbackErrors...)
	}
	transaction.rolledBack = true
	return nil
}

func (transaction *Transaction) verifyServiceDrift(ctx context.Context) error {
	var drift []error
	for _, record := range []*serviceRecord{
		&transaction.dns,
		&transaction.resolver,
	} {
		if record.Restored {
			continue
		}
		current, err := transaction.backend.ServiceState(ctx, record.Name)
		if err != nil {
			drift = append(drift, err)
			continue
		}
		if current != record.Expected {
			drift = append(drift, fmt.Errorf(
				"service %s changed state after host integration",
				record.Name,
			))
		}
	}
	return errors.Join(drift...)
}

func (transaction *Transaction) restoreService(
	ctx context.Context,
	record *serviceRecord,
	configurationChanged bool,
) error {
	if record.Restored {
		return nil
	}
	current, err := transaction.backend.ServiceState(ctx, record.Name)
	if err != nil {
		return err
	}
	switch {
	case record.Prior.Active && configurationChanged:
		err = transaction.backend.RestartService(ctx, record.Name)
	case record.Prior.Active && !current.Active:
		err = transaction.backend.StartService(ctx, record.Name)
	case !record.Prior.Active && current.Active:
		err = transaction.backend.StopService(ctx, record.Name)
	}
	if err != nil {
		return fmt.Errorf("restore service %s: %w", record.Name, err)
	}
	current, err = transaction.backend.ServiceState(ctx, record.Name)
	if err != nil {
		return err
	}
	if current != record.Prior {
		return fmt.Errorf(
			"service %s did not return to its prior state",
			record.Name,
		)
	}
	record.Restored = true
	return nil
}

func (transaction *Transaction) abort(cause error) error {
	if rollbackErr := transaction.Rollback(); rollbackErr != nil {
		return errors.Join(cause, fmt.Errorf(
			"rollback host integration: %w",
			rollbackErr,
		))
	}
	return cause
}

func verifyDNS(
	ctx context.Context,
	backend Backend,
	baseDomain string,
) error {
	for _, hostname := range []string{
		baseDomain,
		"docklane-preflight." + baseDomain,
	} {
		addresses, err := backend.LookupHost(ctx, hostname)
		if err != nil {
			return fmt.Errorf("resolve managed hostname %s: %w", hostname, err)
		}
		if len(addresses) == 0 {
			return fmt.Errorf("managed hostname %s returned no addresses", hostname)
		}
		for _, address := range addresses {
			ip := net.ParseIP(address)
			if ip == nil || !ip.Equal(net.ParseIP("127.0.0.1")) {
				return fmt.Errorf(
					"managed hostname %s resolved outside 127.0.0.1: %s",
					hostname,
					address,
				)
			}
		}
	}
	return nil
}

func validDomain(value string) bool {
	if value == "" || len(value) > 253 || !strings.Contains(value, ".") {
		return false
	}
	for _, label := range strings.Split(value, ".") {
		if label == "" || len(label) > 63 ||
			label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, character := range label {
			if (character >= 'a' && character <= 'z') ||
				(character >= '0' && character <= '9') ||
				character == '-' {
				continue
			}
			return false
		}
	}
	return true
}
