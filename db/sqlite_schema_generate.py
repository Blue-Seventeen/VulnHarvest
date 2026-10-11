"""Regenerate the standalone SQLite schema from an applied PostgreSQL metadata export.
Usage: python db/sqlite_schema_generate.py <pg-schema-metadata.json>
This is build-time tooling; no runtime SQL rewriting takes place.
"""
import json, re, sys
from pathlib import Path
meta=json.loads(Path(sys.argv[1]).read_text(encoding='utf-8-sig'))
root=Path(__file__).resolve().parent

def expression(s):
    s=re.sub(r'::(?:text|jsonb|bigint|integer|numeric|double precision)(?:\[\])?', '', s)
    s=re.sub(r'= ANY \(ARRAY\[(.*?)\]\)', lambda m:'IN ('+m[1]+')', s)
    s=s.replace("key ~ '^[a-z][a-z0-9_]*$'", "key GLOB '[a-z]*' AND key NOT GLOB '*[^a-z0-9_]*'")
    return s

out=['-- VulnHarvest SQLite schema v1. Independent runtime DDL.',
     '-- Generated from the final applied PG shape including every ALTER, plus runtime LLM tables.',
     '-- GIN/GIST become ordinary text indexes; JSON/network membership uses explicit DAO dialects.',
     'CREATE TABLE IF NOT EXISTS portable_sequences(name TEXT PRIMARY KEY NOT NULL, value INTEGER NOT NULL);']
tables=sorted({c['table_name'] for c in meta['columns']})
for table in tables:
    cols=[c for c in meta['columns'] if c['table_name']==table]
    constraints=[c for c in meta['constraints'] if c['table_name']==table]
    body=[]
    serial_pk=None
    for c in cols:
        name=c['column_name']; default=c['column_default']; typ=c['data_type']
        serial=default and default.startswith('nextval(')
        is_pk=any(x['type']=='p' and x['definition']=='PRIMARY KEY ('+name+')' for x in constraints)
        if serial and is_pk:
            body.append(name+' INTEGER PRIMARY KEY AUTOINCREMENT');serial_pk=name;continue
        mapped={'bigint':'INTEGER','integer':'INTEGER','boolean':'BOOLEAN','double precision':'REAL','timestamp with time zone':'DATETIME','ARRAY':'TEXT','jsonb':'TEXT','cidr':'TEXT'}.get(typ,'TEXT')
        decl=name+' '+mapped+(' NOT NULL' if c['is_nullable']=='NO' else '')
        if serial: default='0'
        elif default:
            if typ=='ARRAY':default="'[]'"
            elif default=='now()': default="(now())"
            else:default=expression(default)
        if default is not None:decl+=' DEFAULT '+default
        if typ in ('jsonb','ARRAY'):
            check='json_valid('+name+')'
            if typ=='ARRAY':check+=" AND json_type("+name+")='array'"
            decl+=' CHECK ('+name+' IS NULL OR ('+check+'))'
        body.append(decl)
    for c in constraints:
        if c['type']=='p' and c['definition']=='PRIMARY KEY ('+str(serial_pk)+')':continue
        body.append('CONSTRAINT '+c['name']+' '+expression(c['definition']))
    out.append('CREATE TABLE IF NOT EXISTS '+table+' (\n    '+',\n    '.join(body)+'\n);')
constraint_indexes={c['name'] for c in meta['constraints'] if c['type'] in ('p','u')}
for idx in meta['indexes']:
    if idx['indexname'] in constraint_indexes:continue
    sql=expression(idx['indexdef']).replace('ON public.','ON ')
    sql=re.sub(r' USING (?:btree|gin|gist)', '', sql)
    sql=re.sub(r'\b(?:inet_ops|jsonb_path_ops)\b','',sql)
    sql=re.sub(r' INCLUDE \(([^)]+)\)','',sql)
    sql=sql.replace('CREATE UNIQUE INDEX ','CREATE UNIQUE INDEX IF NOT EXISTS ').replace('CREATE INDEX ','CREATE INDEX IF NOT EXISTS ')
    out.append(sql+';')
for tr in meta['triggers']:
    if tr['action_statement']!='EXECUTE FUNCTION set_updated_at()':continue
    table=tr['event_object_table'];name=tr['trigger_name']
    out.append('CREATE TRIGGER IF NOT EXISTS '+name+' AFTER UPDATE ON '+table+" BEGIN UPDATE "+table+" SET updated_at=now() WHERE rowid=NEW.rowid; END;")
for event in ['INSERT','UPDATE OF task_ids']:
    suffix='insert' if event=='INSERT' else 'update'
    out.append("""CREATE TRIGGER IF NOT EXISTS trg_assets_task_links_%s AFTER %s ON assets BEGIN
    INSERT INTO task_asset_links(task_id,asset_id,source,source_summary)
    SELECT t.id,NEW.id,'system','任务执行期间自动关联' FROM json_each(NEW.task_ids) j JOIN tasks t ON t.id=j.value AND t.deleted_at IS NULL
    WHERE true ON CONFLICT(task_id,asset_id) DO NOTHING;
    DELETE FROM task_asset_links WHERE asset_id=NEW.id AND task_id NOT IN (SELECT value FROM json_each(NEW.task_ids));
END;"""%(suffix,event))
out.append("""CREATE TRIGGER IF NOT EXISTS trg_conversation_retest_delete BEFORE DELETE ON conversations BEGIN
    UPDATE finding_retests SET status='stopped',error='复测会话已删除',finished_at=now()
    WHERE conversation_id=OLD.id AND status IN ('pending','running');
END;""")
out.append("""CREATE TRIGGER IF NOT EXISTS trg_side_request_ordinal AFTER INSERT ON side_question_requests WHEN NEW.ordinal=0 BEGIN
    INSERT INTO portable_sequences(name,value) VALUES('side_request_ordinal',1)
    ON CONFLICT(name) DO UPDATE SET value=MAX(value,(SELECT COALESCE(MAX(ordinal),0) FROM side_question_requests))+1;
    UPDATE side_question_requests SET ordinal=(SELECT value FROM portable_sequences WHERE name='side_request_ordinal') WHERE id=NEW.id;
END;""")
for filename,constant in [('commands.go','llmRecordsSchema'),('llm_usage.go','llmUsageSchema')]:
    text=(root/filename).read_text(encoding='utf-8-sig')
    sql=re.search(r'const '+constant+r' = `([^`]+)`',text)[1]
    sql=sql.replace('BIGSERIAL PRIMARY KEY','INTEGER PRIMARY KEY AUTOINCREMENT').replace('TIMESTAMPTZ','DATETIME').replace('now()',"(now())")
    out.append(sql.strip())
with (root/'sqlite_schema.sql').open('w',encoding='utf-8',newline='\n') as output:
    output.write('\n\n'.join(out)+'\n')
print('SQLite schema:',len(tables)+2,'business tables;',len(meta['columns']),'PG columns folded into final shape')
