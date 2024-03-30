package nirvanatratum

import (
	"context"
	"fmt"
	"time"

	"github.com/Nirvana-Chain/nirvanad-stratum-bridge/src/gostratum"
	"github.com/Nirvana-Chain/nirvanad/app/appmessage"
	"github.com/Nirvana-Chain/nirvanad/infrastructure/network/rpcclient"
	"github.com/pkg/errors"
	"go.uber.org/zap"
)

type NirvanaApi struct {
	address       string
	blockWaitTime time.Duration
	logger        *zap.SugaredLogger
	Nirvanad      *rpcclient.RPCClient
	connected     bool
}

func NewNirvanaAPI(address string, blockWaitTime time.Duration, logger *zap.SugaredLogger) (*NirvanaApi, error) {
	client, err := rpcclient.NewRPCClient(address)
	if err != nil {
		return nil, err
	}

	return &NirvanaApi{
		address:       address,
		blockWaitTime: blockWaitTime,
		logger:        logger.With(zap.String("component", "nirvanaapi:"+address)),
		Nirvanad:      client,
		connected:     true,
	}, nil
}

func (ks *NirvanaApi) Start(ctx context.Context, blockCb func()) {
	ks.waitForSync(true)
	go ks.startBlockTemplateListener(ctx, blockCb)
	go ks.startStatsThread(ctx)
}

func (ks *NirvanaApi) startStatsThread(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	for {
		select {
		case <-ctx.Done():
			ks.logger.Warn("context cancelled, stopping stats thread")
			return
		case <-ticker.C:
			dagResponse, err := ks.Nirvanad.GetBlockDAGInfo()
			if err != nil {
				ks.logger.Warn("failed to get network hashrate from nirvana, prom stats will be out of date", zap.Error(err))
				continue
			}
			response, err := ks.Nirvanad.EstimateNetworkHashesPerSecond(dagResponse.TipHashes[0], 1000)
			if err != nil {
				ks.logger.Warn("failed to get network hashrate from nirvana, prom stats will be out of date", zap.Error(err))
				continue
			}
			RecordNetworkStats(response.NetworkHashesPerSecond, dagResponse.BlockCount, dagResponse.Difficulty)
		}
	}
}

func (ks *NirvanaApi) reconnect() error {
	if ks.Nirvanad != nil {
		return ks.Nirvanad.Reconnect()
	}

	client, err := rpcclient.NewRPCClient(ks.address)
	if err != nil {
		return err
	}
	ks.Nirvanad = client
	return nil
}

func (s *NirvanaApi) waitForSync(verbose bool) error {
	if verbose {
		s.logger.Info("checking Nirvanad sync state")
	}
	for {
		clientInfo, err := s.Nirvanad.GetInfo()
		if err != nil {
			return errors.Wrapf(err, "error fetching server info from Nirvanad @ %s", s.address)
		}
		if clientInfo.IsSynced {
			break
		}
		s.logger.Warn("Nirvana is not synced, waiting for sync before starting bridge")
		time.Sleep(5 * time.Second)
	}
	if verbose {
		s.logger.Info("Nirvanad synced, starting server")
	}
	return nil
}

func (s *NirvanaApi) startBlockTemplateListener(ctx context.Context, blockReadyCb func()) {
	blockReadyChan := make(chan bool)
	err := s.Nirvanad.RegisterForNewBlockTemplateNotifications(func(_ *appmessage.NewBlockTemplateNotificationMessage) {
		blockReadyChan <- true
	})
	if err != nil {
		s.logger.Error("fatal: failed to register for block notifications from nirvana")
	}

	ticker := time.NewTicker(s.blockWaitTime)
	for {
		if err := s.waitForSync(false); err != nil {
			s.logger.Error("error checking Nirvanad sync state, attempting reconnect: ", err)
			if err := s.reconnect(); err != nil {
				s.logger.Error("error reconnecting to Nirvanad, waiting before retry: ", err)
				time.Sleep(5 * time.Second)
			}
		}
		select {
		case <-ctx.Done():
			s.logger.Warn("context cancelled, stopping block update listener")
			return
		case <-blockReadyChan:
			blockReadyCb()
			ticker.Reset(s.blockWaitTime)
		case <-ticker.C: // timeout, manually check for new blocks
			blockReadyCb()
		}
	}
}

func (ks *NirvanaApi) GetBlockTemplate(
	client *gostratum.StratumContext) (*appmessage.GetBlockTemplateResponseMessage, error) {
	template, err := ks.Nirvanad.GetBlockTemplate(client.WalletAddr,
		fmt.Sprintf(`'%s' via Nirvana-Chain/nirvanad-stratum-bridge_%s`, client.RemoteApp, version))
	if err != nil {
		return nil, errors.Wrap(err, "failed fetching new block template from nirvana")
	}
	return template, nil
}
