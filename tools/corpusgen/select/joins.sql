SELECT * FROM t JOIN u ON t.a = u.a
SELECT * FROM t INNER JOIN u ON t.a = u.a
SELECT * FROM t LEFT JOIN u ON t.a = u.a
SELECT * FROM t LEFT OUTER JOIN u ON t.a = u.a
SELECT * FROM t RIGHT JOIN u ON t.a = u.a
SELECT * FROM t RIGHT OUTER JOIN u ON t.a = u.a
SELECT * FROM t FULL JOIN u ON t.a = u.a
SELECT * FROM t FULL OUTER JOIN u ON t.a = u.a
SELECT * FROM t CROSS JOIN u
SELECT * FROM t NATURAL JOIN u
SELECT * FROM t NATURAL INNER JOIN u
SELECT * FROM t NATURAL LEFT JOIN u
SELECT * FROM t NATURAL RIGHT JOIN u
SELECT * FROM t NATURAL FULL JOIN u
SELECT * FROM t JOIN u USING (a)
SELECT * FROM t JOIN u USING (a, b)
SELECT * FROM t LEFT JOIN u USING (a)
SELECT * FROM a JOIN b ON a.id = b.id JOIN c ON b.id = c.id
SELECT * FROM a JOIN b ON a.id = b.id LEFT JOIN c ON b.id = c.id RIGHT JOIN d ON c.id = d.id
SELECT * FROM a LEFT JOIN b ON a.id = b.id LEFT JOIN c ON a.id = c.id LEFT JOIN d ON a.id = d.id
SELECT * FROM a JOIN b JOIN c ON b.id = c.id ON a.id = b.id
SELECT * FROM a LEFT JOIN b JOIN c ON b.id = c.id ON a.id = b.id
SELECT * FROM a JOIN b LEFT JOIN c JOIN d ON c.id = d.id ON b.id = c.id ON a.id = b.id
SELECT * FROM (a JOIN b ON a.id = b.id)
SELECT * FROM (a JOIN b ON a.id = b.id) JOIN c ON b.id = c.id
SELECT * FROM a JOIN (b JOIN c ON b.id = c.id) ON a.id = b.id
SELECT * FROM (a JOIN b ON a.id = b.id) JOIN (c JOIN d ON c.id = d.id) ON b.id = c.id
SELECT * FROM ((a JOIN b ON a.id = b.id) JOIN c ON b.id = c.id) JOIN d ON c.id = d.id
SELECT * FROM (a JOIN b ON a.id = b.id) AS x
SELECT * FROM (a JOIN b ON a.id = b.id) AS x (p, q)
SELECT * FROM a CROSS JOIN b CROSS JOIN c
SELECT * FROM a CROSS JOIN b JOIN c ON b.id = c.id
SELECT * FROM a NATURAL JOIN b NATURAL JOIN c
SELECT * FROM a, b JOIN c ON b.id = c.id
SELECT * FROM a JOIN b ON a.id = b.id, c
SELECT * FROM a, b CROSS JOIN c, d
SELECT * FROM t AS x JOIN t AS y ON x.a = y.a
SELECT * FROM t x JOIN u y ON x.a = y.a
SELECT * FROM t JOIN (SELECT a FROM u) AS x ON t.a = x.a
SELECT * FROM (SELECT a FROM t) AS x JOIN (SELECT a FROM u) AS y ON x.a = y.a
SELECT * FROM t LEFT JOIN (SELECT a FROM u) AS x ON t.a = x.a
SELECT * FROM t JOIN u ON t.a = u.a AND t.b > u.b
SELECT * FROM t JOIN u ON t.a = u.a OR t.b = u.b
SELECT * FROM t JOIN u ON (t.a = u.a)
SELECT * FROM t JOIN u ON t.a IN (SELECT id FROM c)
SELECT * FROM t JOIN u ON EXISTS (SELECT 1 FROM c WHERE c.id = t.a)
SELECT * FROM t JOIN u ON t.a BETWEEN u.a AND u.b
SELECT * FROM t JOIN u ON true
SELECT * FROM t JOIN f(1) AS x ON t.a = x.a
SELECT * FROM t CROSS JOIN LATERAL f(t.a) AS x
SELECT * FROM t JOIN u USING (a) AS x
SELECT * FROM t JOIN u
SELECT * FROM t LEFT JOIN u
SELECT * FROM t INNER JOIN u
SELECT * FROM t JOIN u ON
SELECT * FROM t JOIN u USING
SELECT * FROM t JOIN u USING ()
SELECT * FROM t JOIN u USING (a
SELECT * FROM t JOIN u USING (a,)
SELECT * FROM t JOIN ON t.a = u.a
SELECT * FROM JOIN u ON t.a = u.a
SELECT * FROM t LEFT u ON t.a = u.a
SELECT * FROM t OUTER JOIN u ON t.a = u.a
SELECT * FROM t LEFT RIGHT JOIN u ON t.a = u.a
SELECT * FROM t INNER LEFT JOIN u ON t.a = u.a
SELECT * FROM t NATURAL JOIN u ON t.a = u.a
SELECT * FROM t NATURAL JOIN u USING (a)
SELECT * FROM t CROSS JOIN u ON t.a = u.a
SELECT * FROM t CROSS JOIN u USING (a)
SELECT * FROM t JOIN u ON t.a = u.a ON t.b = u.b
SELECT * FROM t JOIN u USING (a) ON t.b = u.b
SELECT * FROM a JOIN b JOIN c ON b.id = c.id
SELECT * FROM (a JOIN b)
SELECT * FROM (a JOIN b ON a.id = b.id
SELECT * FROM a JOIN (b JOIN c) ON a.id = b.id
