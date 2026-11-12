# The best customer of each country and month since @since,
# with the revenue per web session.
WITH monthly AS (
  SELECT
    FORMAT_DATE('%Y-%m', DATE(o.created_at)) AS month,
    c.country,
    o.customer_id,
    SUM(i.quantity * p.price) AS revenue
  FROM `acme-shop.sales.orders` AS o
  JOIN `acme-shop.sales.order_items` AS i ON i.order_id = o.id
  JOIN `acme-shop.sales.products` AS p ON p.id = i.product_id
  JOIN `acme-shop.crm.active_customers` AS c ON c.id = o.customer_id
  WHERE o.created_at >= @since AND o.status != 'cancelled'
  GROUP BY month, c.country, o.customer_id
),
visits AS (
  SELECT customer_id, COUNT(*) AS sessions
  FROM `acme-shop.web.sessions`
  WHERE started_at >= @since
  GROUP BY customer_id
)
SELECT
  m.month,
  m.country,
  m.customer_id,
  m.revenue,
  SAFE_DIVIDE(m.revenue, v.sessions) AS revenue_per_session
FROM monthly AS m
LEFT JOIN visits AS v USING (customer_id)
WHERE m.country IN ('PL', 'DE', 'US')
QUALIFY ROW_NUMBER() OVER (PARTITION BY m.month, m.country ORDER BY m.revenue DESC) = 1
ORDER BY m.month, m.revenue DESC;
