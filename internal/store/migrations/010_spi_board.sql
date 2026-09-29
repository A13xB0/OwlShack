-- The SPI radio hat's wiring; NULL for a KISS modem, which is every install before this.

ALTER TABLE settings ADD COLUMN spi_board TEXT;
