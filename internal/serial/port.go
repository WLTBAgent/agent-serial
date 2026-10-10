package serial

import (
	"fmt"

	"go.bug.st/serial/enumerator"
)

// PortInfo describes one serial port on the system. For USB-serial adapters
// the USB identity fields are populated.
type PortInfo struct {
	Name         string `json:"name"`
	Description  string `json:"description,omitempty"`
	USBID        string `json:"usb_id,omitempty"` // e.g. "1a86:7523"
	Product      string `json:"product,omitempty"`
	SerialNumber string `json:"serial_number,omitempty"`
}

// ListPorts enumerates serial ports, with USB details where available.
func ListPorts() ([]PortInfo, error) {
	detailed, err := enumerator.GetDetailedPortsList()
	if err != nil {
		return nil, fmt.Errorf("enumerate ports: %w", err)
	}
	out := make([]PortInfo, 0, len(detailed))
	for _, p := range detailed {
		info := PortInfo{
			Name:         p.Name,
			Description:  p.Product, // go.bug.st: Product is the USB product string
			Product:      p.Product,
			SerialNumber: p.SerialNumber,
		}
		if p.IsUSB {
			info.USBID = fmt.Sprintf("%s:%s", p.VID, p.PID)
		}
		if info.Description == "" {
			info.Description = "serial port"
		}
		out = append(out, info)
	}
	return out, nil
}
