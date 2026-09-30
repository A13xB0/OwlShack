-- openHop firmware runs its serial link at 921600 on every board, so no other rate was ever answered through a USB-UART bridge.
UPDATE settings SET baud_rate = 921600 WHERE connection LIKE 'openhop:///%' AND baud_rate IS NOT 921600;
