-- The TX duty cycle as a percentage, the firmware's `set dutycycle` unit; NULL is the library default of 50%.

ALTER TABLE settings ADD COLUMN duty_cycle_pct REAL;
