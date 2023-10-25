package main

import (
	"errors"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	berrylan "github.com/basilfx/go-ble-berrylan"

	log "github.com/sirupsen/logrus"
)

var (
	newCellRegexp = regexp.MustCompile(`^Cell\s+(?P<cell_number>.+)\s+-\s+Address:\s(?P<mac>.+)$`)
	regxp         [6]*regexp.Regexp
)

type Cell struct {
	CellNumber     string  `json:"cell_number"`
	MAC            string  `json:"mac"`
	ESSID          string  `json:"essid"`
	Frequency      float32 `json:"frequency"`
	FrequencyUnits string  `json:"frequency_units"`
	EncryptionKey  bool    `json:"encryption_key"`
	Encryption     string  `json:"encryption"`
	SignalLevel    int     `json:"signal_level"`
}

type Cells struct {
	Cells []Cell `json:"cells"`
}

func init() {
	// precompile regexp
	regxp = [6]*regexp.Regexp{
		regexp.MustCompile(`^ESSID:\"(?P<essid>.*)\"$`),
		regexp.MustCompile(`^Frequency:(?P<frequency>[\d.]+) (?P<frequency_units>.+) \(Channel (?P<channel>\d+)\)$`),
		regexp.MustCompile(`^Encryption key:(?P<encryption_key>.+)$`),
		regexp.MustCompile(`^IE:\ WPA\ Version\ (?P<wpa>.+)$`),
		regexp.MustCompile(`^IE:\ IEEE\ 802\.11i/WPA2\ Version\ (?P<wpa2>)$`),
		regexp.MustCompile(`^Quality=(?P<signal_quality>\d+)/(?P<signal_total>\d+)\s+Signal level=(?P<signal_level>.+) d.+$`),
	}
}

// WirelessInterface represents a wireless interface.
type WirelessInterface struct {
	connected bool
	ssid      string

	connectionStatusUpdateHandler berrylan.ConnectionStatusUpdateHandler
}

// NewDummyWirelessInterface initializes a new instance of
// DummyWirelessInterface that simulates a wireless interface.
func NewDummyWirelessInterface() *WirelessInterface {
	return &WirelessInterface{}
}

func (d *WirelessInterface) onConnectionUpdate(s berrylan.WirelessConnectionStatus) {
	log.Infof("Changing network status to '%s'.", s.String())

	if d.connectionStatusUpdateHandler != nil {
		d.connectionStatusUpdateHandler(s)
	}
}

// StartAccessPoint implements berrylan.WirelessInterface.StartAccessPoint by
// returning an error because it is not supported.
func (d *WirelessInterface) StartAccessPoint(ssid string, passphrase string) error {
	return errors.New("not supported")
}

// Test implements berrylan.WirelessInterface.Test by returning an error
// because it is not supported.
func (d *WirelessInterface) Test(ssid string) error {
	return errors.New("not supported")
}

// GetConnection implements berrylan.WirelessInterface.GetConnection by
// returning dummy connection information (if connected).
func (d *WirelessInterface) GetConnection() *berrylan.ConnectionInfo {
	if d.connected {
		return &berrylan.ConnectionInfo{
			Ssid:           d.ssid,
			MACAddress:     "00:00:00:00:00:00",
			IPAddress:      "127.0.0.1",
			Protected:      true,
			SignalStrength: 50,
		}
	}

	return nil
}

// GetNetworks implements berrylan.WirelessInterface.GetNetworks by returning
// two networks.
func (d *WirelessInterface) GetNetworks() []berrylan.NetworkInfo {

	cells, err := Scan()
	if err != nil {
		panic(err)
	}
	// fmt.Println(cells)

	var networks []berrylan.NetworkInfo
	for _, cell := range cells.Cells {
		networks = append(networks, berrylan.NetworkInfo{
			Ssid:           cell.ESSID,
			MACAddress:     cell.MAC,
			Protected:      cell.EncryptionKey,
			SignalStrength: cell.SignalLevel,
		})
	}

	return networks
}

// ScanNetwork implements berrylan.WirelessInterface.ScanNetwork by retuning
// nothing because it is not implemented.
func (d *WirelessInterface) ScanNetwork() {
	return
}

// Connect implements berrylan.WirelessInterface.Connect by simulating a
// connection request to a given network.
func (d *WirelessInterface) Connect(ssid string, passphrase string, hidden bool) error {
	log.Infof(
		"Connecting to '%s' with passpharse is '%s'",
		ssid,
		passphrase)

	d.ssid = ssid

	go func() {
		d.onConnectionUpdate(berrylan.WirelessConnectionStatusPrepare)
		time.Sleep(2 * time.Second)
		d.onConnectionUpdate(berrylan.WirelessConnectionStatusSecondaries)
		time.Sleep(2 * time.Second)

		d.connected = true

		d.onConnectionUpdate(berrylan.WirelessConnectionStatusActivated)
	}()

	return nil
}

// Disconnect implements berrylan.WirelessInterface.Disconnect by simulating a
// disconnection request.
func (d *WirelessInterface) Disconnect() error {
	log.Infof("Disconnecting from network.")

	d.ssid = ""

	go func() {
		d.connectionStatusUpdateHandler(
			berrylan.WirelessConnectionStatusDeactivating)
		time.Sleep(1 * time.Second)

		d.connected = false

		d.connectionStatusUpdateHandler(
			berrylan.WirelessConnectionStatusDisconnected)
	}()

	return nil
}

// HandleConnectionStatusUpdate implements
// berrylan.WirelessInterface.HandleConnectionStatusUpdate by storing a
// reference to an event handler function.
func (d *WirelessInterface) HandleConnectionStatusUpdate(f berrylan.ConnectionStatusUpdateHandler) {
	d.connectionStatusUpdateHandler = f
}

func Scan() (Cells, error) {
	// execute iwlist for scanning wireless networks
	cmd := exec.Command("iwlist", "scan")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return Cells{}, err
	}

	// parse fetched result
	return parse(string(out)), nil
}

func parse(input string) Cells {
	lines := strings.Split(input, "\n")

	var cells Cells
	var cell *Cell
	var wg sync.WaitGroup
	var m sync.Mutex

	for _, line := range lines {
		line = strings.TrimSpace(line)

		// check new cell value
		if cellValues := newCellRegexp.FindStringSubmatch(line); len(cellValues) > 0 {
			//if essid is null don't add cell
			if cell != nil && cell.ESSID == "" {
				continue
			}

			cells.Cells = append(cells.Cells, Cell{
				CellNumber: cellValues[1],
				MAC:        cellValues[2],
			})
			cell = &cells.Cells[len(cells.Cells)-1]

			continue
		}

		// compare lines to regexps
		wg.Add(len(regxp))
		for _, reg := range regxp {
			go compare(line, &wg, &m, cell, reg)
		}
		wg.Wait()
	}

	return cells
}

func compare(line string, wg *sync.WaitGroup, m *sync.Mutex, cell *Cell, reg *regexp.Regexp) {
	defer wg.Done()

	if values := reg.FindStringSubmatch(line); len(values) > 0 {
		keys := reg.SubexpNames()

		m.Lock()

		for i := 1; i < len(keys); i++ {
			switch keys[i] {
			case "essid":
				cell.ESSID = values[i]
			case "frequency":
				if frequency, err := strconv.ParseFloat(values[i], 32); err == nil {
					cell.Frequency = float32(frequency)
				}
			case "frequency_units":
				cell.FrequencyUnits = values[i]
			case "encryption_key":
				if cell.EncryptionKey = values[i] == "on"; cell.EncryptionKey {
					cell.Encryption = "wep"
				} else {
					cell.Encryption = "off"
				}
			case "wpa":
				cell.Encryption = "wpa"
			case "wpa2":
				cell.Encryption = "wpa2"
			case "signal_level":
				if level, err := strconv.ParseInt(values[i], 10, 32); err == nil {
					cell.SignalLevel = int(level)
				}
			}
		}

		m.Unlock()
	}
}
