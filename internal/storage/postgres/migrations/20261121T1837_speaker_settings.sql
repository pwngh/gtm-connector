-- A workspace's speaker settings, beside its seller list: the company's own email domains and the
-- aliases that map a speaker label to a person. connect saves them, and every card seals them in its
-- evidence. The connector checks each entry before it saves the object; the database keeps it a JSON
-- object of bounded size.
ALTER TABLE workspaces ADD COLUMN speakers text NOT NULL DEFAULT '{}'
    CHECK (octet_length(speakers) <= 65536 AND jsonb_typeof(speakers::jsonb) = 'object');

-- A card whose org has more Salesforce users than a card reads states that its users were not read. The
-- list holds every code deal_matches added too, so it is the same whichever of the two applied first.
ALTER TABLE gaps DROP CONSTRAINT gaps_code_check;
ALTER TABLE gaps ADD CONSTRAINT gaps_code_check CHECK (code IN (
    'crm_unmapped_fields', 'calls_denied', 'calls_excerpt', 'calls_outside_records',
    'slack_not_requested', 'slack_denied', 'slack_thread', 'slack_channel',
    'calls_not_connected', 'slack_not_connected', 'slack_not_selected',
    'calls_not_matched', 'slack_not_matched',
    'crm_more_opportunities', 'crm_more_contracts', 'crm_more_contacts', 'sentences_cut',
    'sentences_more', 'sentences_long', 'sentences_later', 'crm_fields_empty',
    'crm_no_opportunity', 'calls_no_record', 'slack_no_message', 'calls_no_transcript',
    'speakers_unknown', 'neutralized', 'calls_no_contact', 'crm_users_not_read'));
