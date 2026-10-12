// Loaded once in the page shell. The server renders the ZPL from the DB template; this sends it over WebUSB.
// The header (printer button, toast) sits outside #app, so the connection survives tab changes.
const $ = id => document.getElementById(id);
let toastTimer;
const log = (m, bad) => {
  const t = $('toast'); t.textContent = m; t.classList.add('show');
  clearTimeout(toastTimer); toastTimer = setTimeout(() => t.classList.remove('show'), bad === false ? 3000 : 6000);
};

class WebUsbTransport {
  constructor() { this.device = null; this.epOut = null; }
  static supported() { return 'usb' in navigator; }

  async connect() {
    const device = await navigator.usb.requestDevice({
      filters: [{ classCode: 7 }, { vendorId: 0x0a5f }, { vendorId: 0x0fe6 }, { vendorId: 0x0483 }]
    });
    await this.open(device);
  }
  async reconnect() {
    const devices = await navigator.usb.getDevices();
    if (devices.length) { await this.open(devices[0]); return true; }
    return false;
  }
  async open(device) {
    await device.open();
    if (device.configuration === null) await device.selectConfiguration(1);
    let found = null;
    for (const itf of device.configuration.interfaces) {
      for (const alt of itf.alternates) {
        const out = alt.endpoints.find(e => e.direction === 'out' && e.type === 'bulk');
        if (out && (!found || alt.interfaceClass === 7)) found = { itf, alt, out };
      }
    }
    if (!found) throw new Error('No bulk OUT endpoint found');
    await device.claimInterface(found.itf.interfaceNumber);
    if (found.alt.alternateSetting !== 0) await device.selectAlternateInterface(found.itf.interfaceNumber, found.alt.alternateSetting);
    this.device = device; this.epOut = found.out.endpointNumber;
  }
  get isConnected() { return !!(this.device && this.device.opened); }
  get name() { return this.device ? `${this.device.manufacturerName || ''} ${this.device.productName || 'USB device'}`.trim() : ''; }
  async send(data) {
    const bytes = typeof data === 'string' ? new TextEncoder().encode(data) : data;
    for (let i = 0; i < bytes.length; i += 16384) {
      const res = await this.device.transferOut(this.epOut, bytes.slice(i, i + 16384));
      if (res.status !== 'ok') throw new Error('transferOut status: ' + res.status);
    }
  }
  async close() { if (this.device) { try { await this.device.close(); } catch {} } this.device = null; }
}

const printer = new WebUsbTransport();
const refresh = () => {
  const b = $('connect'); b.classList.toggle('on', printer.isConnected);
  b.textContent = printer.isConnected ? 'Printer: ' + printer.name : 'Printer: not connected';
};

$('connect').onclick = async () => {
  try { await printer.connect(); log('Connected: ' + printer.name, false); } catch (e) { log('Connect failed: ' + e.message); }
  refresh();
};

if (!WebUsbTransport.supported()) {
  log('WebUSB is not available. Use Chrome, Edge or Opera on https:// or localhost.');
  $('connect').disabled = true;
} else {
  navigator.usb.addEventListener('disconnect', () => { printer.close(); refresh(); log('Printer unplugged'); });
  printer.reconnect().then(ok => { if (ok) log('Reconnected to ' + printer.name); refresh(); }).catch(e => log('Auto-reconnect failed: ' + e.message));
}

// Don't ask the server to render if there is nowhere to send the result.
document.body.addEventListener('htmx:confirm', e => {
  if (e.detail.elt.id === 'print-form' && !printer.isConnected) { e.preventDefault(); log('Select the printer first.'); }
});

// The /print/render response body is the finished ZPL.
document.body.addEventListener('htmx:afterRequest', async e => {
  if (e.detail.elt.id !== 'print-form') return;
  const xhr = e.detail.xhr;
  if (!e.detail.successful) return; // the error text is shown by htmx:responseError
  try {
    await printer.send(xhr.responseText);
    const qty = e.detail.elt.elements.qty.value;
    log(`Sent ${qty} label(s) to the printer.`, false);
  } catch (err) { log('Print failed: ' + err.message); }
});

// Server-side validation errors (422 etc.) come back as plain text.
document.body.addEventListener('htmx:responseError', e => log(e.detail.xhr.responseText || 'Request failed'));
