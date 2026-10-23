() => {
  const b64 = 'iVBORw0KGgoAAAANSUhEUgAAAHgAAAB4CAIAAAAX4BpsAAAAWklEQVR4nO3BMQEAAADCoPVPbQ0PoAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA4N8AKgABAV0m9QAAAABJRU5ErkJggg==';
  const bin = atob(b64);
  const arr = new Uint8Array(bin.length);
  for (let i = 0; i < bin.length; i++) arr[i] = bin.charCodeAt(i);
  const file = new File([arr], 'testimg.png', { type: 'image/png' });
  const dt = new DataTransfer();
  dt.items.add(file);
  const ev = new ClipboardEvent('paste', { clipboardData: dt, bubbles: true, cancelable: true });
  document.getElementById('drop').dispatchEvent(ev);
  return 'dispatched';
}
