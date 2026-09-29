-- Who may DM a companion; 'contacts' is what every install did before this.

ALTER TABLE companions ADD COLUMN dm_policy TEXT NOT NULL DEFAULT 'contacts';

ALTER TABLE companions ADD COLUMN dm_allow TEXT NOT NULL DEFAULT '';
