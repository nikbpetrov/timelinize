#!/usr/bin/env python3
"""Print per-source/classification counts for the dev repo (or a repo given as argv[1])."""
import sqlite3, sys
db = (sys.argv[1] if len(sys.argv) > 1 else '/mnt/photos/timelinize/repo-dev') + '/timeline.db'
c = sqlite3.connect(db)
print("jobs:", c.execute("select id, type, state, progress from jobs").fetchall())
rows = c.execute("""select ds.name, cl.name, count(*), sum(i.data_file is null and i.data_text is null)
  from items i join data_sources ds on ds.id=i.data_source_id left join classifications cl on cl.id=i.classification_id
  group by 1,2 order by 1,2""").fetchall()
for r in rows: print(f"  {r[0]:10s} {str(r[1]):10s} items={r[2]:4d} no_data={r[3]}")
print("entities:", c.execute("select count(*) from entities").fetchone()[0],
      "relationships:", c.execute("select count(*) from relationships").fetchone()[0],
      "mojibake entities:", c.execute("select count(*) from entities where name like '%Ð%'").fetchone()[0])
