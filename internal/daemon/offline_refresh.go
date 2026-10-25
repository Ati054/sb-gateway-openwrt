package daemon

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"

	"github.com/Ati054/sb-gateway-openwrt/internal/generation"
	"github.com/Ati054/sb-gateway-openwrt/internal/refresh"
	"github.com/Ati054/sb-gateway-openwrt/internal/settings"
	"github.com/Ati054/sb-gateway-openwrt/internal/subscription"
)

// One owner may publish the catalog: the running daemon or a temporary
// offline importer. The kernel releases this lock even after SIGKILL.
func acquireCatalog(paths Paths) (*os.File, error) {
	if err := os.MkdirAll(paths.RunRoot, 0700); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(filepath.Join(paths.RunRoot, "catalog.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = lockCatalogFile(file); err != nil {
		file.Close()
		return nil, errors.New("subscription loading or SB Gateway is already running")
	}
	return file, nil
}

func selectedSources(sources []settings.Subscription, id string) ([]settings.Subscription, error) {
	var selected []settings.Subscription
	for _, source := range sources {
		if id == "*" || id == source.ID {
			selected = append(selected, source)
		}
	}
	if len(selected) == 0 {
		return nil, errors.New("unknown subscription source")
	}
	return selected, nil
}

// BeginOfflineRefresh returns immediately to LuCI. The child only imports
// subscription data; it never starts the service or alters the network.
func BeginOfflineRefresh(config settings.Config, paths Paths, id string) error {
	if config.Enabled {
		return errors.New("offline import requires a disabled service")
	}
	selected, err := selectedSources(config.Subscriptions, id)
	if err != nil {
		return err
	}
	lock, err := acquireCatalog(paths)
	if err != nil {
		return err
	}
	defer lock.Close()
	binary, err := os.Executable()
	if err != nil {
		return err
	}
	null, err := os.OpenFile(os.DevNull, os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer null.Close()
	store := generation.Store{Root: paths.DataRoot, MetadataRoot: paths.RunRoot}
	_, nodes, loadErr := store.Current()
	if loadErr != nil && !errors.Is(loadErr, os.ErrNotExist) {
		return loadErr
	}
	updates := newSubscriptionUpdates(paths, config.Subscriptions)
	for _, source := range selected {
		updates.begin(source.ID, nodes)
	}
	child := exec.Command(binary, "refresh-worker", id)
	child.Stdin, child.Stdout, child.Stderr = null, null, null
	// FD 3 transfers ownership without an unlocked gap between processes.
	child.ExtraFiles = []*os.File{lock}
	if err = child.Start(); err != nil {
		for _, source := range selected {
			updates.finish(source.ID, updates.values[source.ID].Attempt, nodes, nodes, err)
		}
		return err
	}
	return child.Process.Release()
}

func OfflineRefreshWorker(ctx context.Context, paths Paths, id string) (result error) {
	lock := os.NewFile(3, "catalog.lock")
	if lock == nil {
		return errors.New("offline import lock is unavailable")
	}
	defer lock.Close()
	// A directly invoked worker must also hold the actual catalog lock.
	info, err := lock.Stat()
	owner, ownerErr := os.Stat(filepath.Join(paths.RunRoot, "catalog.lock"))
	if err != nil || ownerErr != nil || !info.Mode().IsRegular() || !os.SameFile(info, owner) || lockCatalogFile(lock) != nil {
		return errors.New("invalid offline import lock")
	}
	updates := &subscriptionUpdates{path: filepath.Join(paths.RunRoot, "subscription-status.json"), values: readSubscriptionUpdates(paths)}
	defer func() {
		if result != nil {
			store := generation.Store{Root: paths.DataRoot, MetadataRoot: paths.RunRoot}
			_, nodes, _ := store.Current()
			for source, status := range updates.values {
				if (source == id || id == "*") && status.State == "loading" {
					updates.finish(source, status.Attempt, nodes, nodes, result)
				}
			}
		}
	}()
	config, err := settings.ReadSystemSubscriptions(ctx)
	if err != nil {
		return err
	}
	fetch := func(ctx context.Context, source settings.Subscription) ([]byte, error) {
		client := subscription.NewHTTPClient(0)
		defer client.CloseIdleConnections()
		body, _, _, err := subscription.Fetch(ctx, client, source.URL, "")
		return body, err
	}
	verify := func(ctx context.Context, source settings.Subscription) error {
		current, err := settings.ReadSystemSubscriptions(ctx)
		if err != nil {
			return err
		}
		selected, err := selectedSources(current.Subscriptions, source.ID)
		if current.Enabled || err != nil || !reflect.DeepEqual(selected[0], source) {
			return errors.New("subscription settings changed during loading; retry")
		}
		return nil
	}
	return importOffline(ctx, config, paths, id, updates, fetch, verify)
}

func importOffline(ctx context.Context, config settings.Config, paths Paths, id string, updates *subscriptionUpdates,
	fetch func(context.Context, settings.Subscription) ([]byte, error), verify func(context.Context, settings.Subscription) error) error {
	if config.Enabled {
		return errors.New("offline import requires a disabled service")
	}
	selected, err := selectedSources(config.Subscriptions, id)
	if err != nil {
		return err
	}
	service := &refresh.Service{Store: generation.Store{Root: paths.DataRoot, MetadataRoot: paths.RunRoot}}
	var failures []error
	for _, source := range selected {
		_, before, loadErr := service.Store.Current()
		if loadErr != nil && !errors.Is(loadErr, os.ErrNotExist) {
			return loadErr
		}
		attempt := updates.values[source.ID].Attempt
		if updates.values[source.ID].State != "loading" {
			attempt = updates.begin(source.ID, before)
		}
		var body []byte
		if source.FetchVia == "proxy-only" {
			err = errors.New("proxy-only subscription loading requires a running proxy service")
		} else {
			body, err = fetch(ctx, source)
		}
		if err == nil {
			err = verify(ctx, source)
		}
		if err == nil {
			_, err = service.Import(ctx, source.ID, body, source.MaxNodes)
		}
		_, after, _ := service.Store.Current()
		updates.finish(source.ID, attempt, before, after, err)
		if err != nil {
			failures = append(failures, fmt.Errorf("subscription %q: %w", source.ID, err))
		}
	}
	_ = service.Store.Prune(3)
	return errors.Join(failures...)
}
